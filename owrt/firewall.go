package owrt

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// nftTable — приватная nft-таблица для masquerade трафика, уходящего в туннель.
const nftTable = "wgkeybot"

// EnableNAT добавляет приватную nft-таблицу с masquerade на oifname=tunName.
// Это подстраховка к fw4-зоне (uci-defaults): device-зона может не примениться к
// динамически созданному TUN. Идемпотентна; при отсутствии nft — no-op (полагаемся
// на fw4). Своя таблица (inet wgkeybot) не конфликтует с fw4 (inet fw4).
func EnableNAT(tunName string) error {
	if _, err := exec.LookPath("nft"); err != nil {
		log.Printf("[NAT] nft не найден — masquerade через fw4-зону")
		return nil
	}
	DisableNAT() // снять возможную устаревшую таблицу

	// priority 110 — после fw4 srcnat(100); если fw4 уже сделал SNAT, повтор no-op.
	script := fmt.Sprintf(`table inet %[1]s {
	chain postrouting {
		type nat hook postrouting priority 110; policy accept;
		oifname "%[2]s" masquerade
	}
}
`, nftTable, tunName)

	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft masquerade: %v: %s", err, strings.TrimSpace(string(out)))
	}
	log.Printf("[NAT] nft masquerade fallback for %s (table inet %s)", tunName, nftTable)
	return nil
}

// DisableNAT удаляет приватную nft-таблицу. Безопасно при её отсутствии.
func DisableNAT() {
	if _, err := exec.LookPath("nft"); err != nil {
		return
	}
	// Игнорируем ошибку "No such file or directory" если таблицы нет.
	exec.Command("nft", "delete", "table", "inet", nftTable).Run()
}
