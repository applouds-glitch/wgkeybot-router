/* SPDX-License-Identifier: Apache-2.0
 *
 * Copyright © 2026 WireGuard LLC. All Rights Reserved.
 */

package proxy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/turn/v5"
)

// winner holds a successfully allocated TURN session from the race in
// runWithCreds. The fields are the live resources handed off to the session:
// runSession owns them and is responsible for closing them.
type winner struct {
	client *turn.Client
	raw    net.Conn       // underlying dialed UDP/TCP conn
	relay  net.PacketConn // relay allocation from client.Allocate()
	addr   string         // TURN server the session runs on
	rtt    time.Duration  // Dial → Allocate latency
	perm   *permWatch     // control-plane blackhole detector for this session
}

// runWithCreds establishes one TURN session with pre-fetched credentials on the
// stream's assigned server, addrs[0] (see assignServers). There is no latency
// race: only if that server fails to Allocate are the remaining candidates
// dialed, all at once, and the first of those to complete Allocate takes the
// session while the others are cancelled/closed. The assignment is per attempt,
// so the next reconnect goes back to the stream's own server. Retry and
// credential rotation are managed by the calling WorkerGroup.
func (s *stream) runWithCreds(ctx context.Context, user, pass string, addrs []string, cfg WorkerGroupConfig) error {
	s.ready.Store(false)
	defer s.ready.Store(false)

	// raceCtx is cancelled the moment a winner is chosen (or ctx dies) so the
	// losing failover candidates stop dialing / abort their semaphore wait
	// promptly.
	raceCtx, cancelRace := context.WithCancel(ctx)
	defer cancelRace()

	var once sync.Once
	winCh := make(chan winner, 1)
	errCh := make(chan error, len(addrs))
	var wg sync.WaitGroup

	launch := func(addr string) {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			client, raw, relay, rtt, perm, err := dialAndAllocate(raceCtx, s, user, pass, addr, cfg)
			if err != nil {
				errCh <- err
				return
			}
			claimed := false
			once.Do(func() {
				claimed = true
				cancelRace()
				winCh <- winner{client: client, raw: raw, relay: relay, addr: addr, rtt: rtt, perm: perm}
			})
			if !claimed {
				// Another failover candidate got there first: log this
				// server's RTT, then release the surplus allocation
				// (relayConn.Close → Refresh(lifetime=0) deallocates server-side).
				turnLog("[STREAM %d] Failover candidate %s lost rtt=%v (group %d)", s.id, addr, rtt, cfg.GroupID)
				relay.Close()
				client.Close()
				raw.Close()
			}
		}(addr)
	}

	launch(addrs[0])
	launchedCount := 1
	fannedOut := len(addrs) == 1

	fanOut := func() {
		if fannedOut {
			return
		}
		for _, a := range addrs[1:] {
			launch(a)
			launchedCount++
		}
		fannedOut = true
	}

	var lastErr error
	errCount := 0
	for {
		select {
		case w := <-winCh:
			return s.runSession(ctx, w, cfg)
		case err := <-errCh:
			lastErr = err
			errCount++
			if !fannedOut {
				// The assigned server failed: try the failover candidates.
				fanOut()
			} else if errCount == launchedCount {
				return fmt.Errorf("TURN allocate: all %d servers failed: %w", len(addrs), lastErr)
			}
		case <-ctx.Done():
			cancelRace()
			// A racer may still win after we leave; reap it so the allocation
			// and sockets don't leak.
			go reapRace(&wg, winCh)
			return ctx.Err()
		}
	}
}

// dialAndAllocate dials one TURN server and performs the Allocate handshake,
// measuring the Dial→Allocate latency. On any error it closes whatever it
// opened and returns. On success the caller owns client/raw/relay. Uses ctx for
// the dial and the allocSemaphore wait so a cancelled race aborts promptly.
//
// Each attempt gets its own permWatch, wired into the pion logger factory
// before NewClient so it sees the allocation's whole lifecycle. Losing failover
// candidates simply drop theirs — a permWatch owns no goroutine, so an unwatched
// one costs nothing.
func dialAndAllocate(ctx context.Context, s *stream, user, pass, addr string, cfg WorkerGroupConfig) (*turn.Client, net.Conn, net.PacketConn, time.Duration, *permWatch, error) {
	turnLog("[STREAM %d] Dial TURN %s (group %d)", s.id, addr, cfg.GroupID)
	start := time.Now()
	perm := newPermWatch(s.id)

	dialer := &net.Dialer{
		Timeout: 30 * time.Second,
		Control: protectControl,
	}

	var turnConn net.PacketConn
	var raw net.Conn
	if cfg.UseUDP {
		c, err := dialer.DialContext(ctx, "udp", addr)
		if err != nil {
			return nil, nil, nil, 0, nil, fmt.Errorf("TURN UDP dial: %w", err)
		}
		raw = c
		turnConn = &connectedUDPConn{c.(*net.UDPConn)}
	} else {
		c, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, nil, nil, 0, nil, fmt.Errorf("TURN TCP dial: %w", err)
		}
		raw = c
		turnConn = turn.NewSTUNConn(c)
	}

	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: addr,
		TURNServerAddr: addr,
		Username:       user,
		Password:       pass,
		Conn:           turnConn,
		LoggerFactory:  pionLogFactory{streamID: s.id, watch: perm},
	})
	if err != nil {
		raw.Close()
		return nil, nil, nil, 0, nil, fmt.Errorf("TURN client: %w", err)
	}

	if err := client.Listen(); err != nil {
		client.Close()
		raw.Close()
		return nil, nil, nil, 0, nil, fmt.Errorf("TURN listen: %w", err)
	}

	select {
	case allocSemaphore <- struct{}{}:
	case <-ctx.Done():
		client.Close()
		raw.Close()
		return nil, nil, nil, 0, nil, ctx.Err()
	}
	relay, err := client.Allocate()
	<-allocSemaphore
	if err != nil {
		client.Close()
		raw.Close()
		return nil, nil, nil, 0, nil, fmt.Errorf("TURN allocate: %w", err)
	}

	return client, raw, relay, time.Since(start), perm, nil
}

// runSession runs the relay session on the connected server and owns its
// lifecycle: it closes the relay, client and underlying conn on exit.
func (s *stream) runSession(ctx context.Context, w winner, cfg WorkerGroupConfig) error {
	defer w.raw.Close()
	defer w.client.Close()
	defer w.relay.Close()

	turnLog("[STREAM %d] TURN %s rtt=%v (group %d)", s.id, w.addr, w.rtt, cfg.GroupID)
	turnLog("[STREAM %d] Relay: %s", s.id, w.relay.LocalAddr())

	// Blackhole watchdog. When permWatch declares the allocation dead, close the
	// relay: that is the one handle all three transports block on, so whichever
	// loop owns this stream unwinds immediately instead of writing into a dead
	// allocation until the 90s no-RX detector eventually notices — or never
	// notices, because a shaped-but-alive path keeps the liveness clock fresh.
	// Closing here also sends Refresh(lifetime=0), releasing the server-side
	// allocation instead of leaving it to eat the credential's quota.
	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()
	go func() {
		select {
		case <-w.perm.deadCh():
			turnLog("[STREAM %d] Recycling allocation after blackhole", s.id)
			w.relay.Close()
		case <-sessCtx.Done():
		}
	}()

	var err error
	switch cfg.PeerType {
	case "wireguard":
		err = s.runNoDTLS(ctx, w.relay, cfg.PeerAddr)
	case "srtp":
		err = s.runSRTP(ctx, w.relay, cfg.PeerAddr)
	default:
		err = s.runDTLS(ctx, w.relay, cfg.PeerAddr, true)
	}

	// A blackhole teardown surfaces as "use of closed network connection", which
	// carries no diagnosis and — more importantly — no auth/quota wording for
	// classifyCredError to act on. Restate it with pion's own message so a
	// blackhole that was really a stale credential still rotates the credential.
	// A nil err means the loops exited cleanly, which WorkerGroup would read as
	// "server closed the stream": override it too, this was a failure.
	if w.perm.fired() {
		if err == nil {
			return fmt.Errorf("TURN blackhole: %s", w.perm.why())
		}
		return fmt.Errorf("TURN blackhole: %s (%w)", w.perm.why(), err)
	}
	return err
}

// reapRace drains any late-arriving winner (after the caller has given up on
// ctx cancellation) and closes its resources so they don't leak. winCh holds at
// most one winner (guarded by sync.Once), so a single non-blocking drain after
// all dialers have exited is sufficient.
func reapRace(wg *sync.WaitGroup, winCh chan winner) {
	wg.Wait()
	select {
	case w := <-winCh:
		w.relay.Close()
		w.client.Close()
		w.raw.Close()
	default:
	}
}
