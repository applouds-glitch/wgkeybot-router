package core

import (
	"io"
	"log"
	"log/syslog"
	"os"
	"path/filepath"
	"sync"

	"github.com/wgkeybot/router/pkg/proxy"
)

// logMaxBytes — порог ротации; при превышении файл переименовывается в .0.
const logMaxBytes = 1 << 20 // 1 MiB

var (
	logInitMu sync.Mutex
	logF      *os.File
	logSyslog io.Writer
)

// InitLogging направляет журнал демона в файл (с ротацией по размеру),
// stderr и best-effort в syslog роутера; журнал прокси-ядра — в тот же файл.
// Вызывать один раз при старте демона.
//
// Платформа без файла журнала (LogPath == "") не вызывает InitLogging вовсе —
// на OpenWrt stdout забирает procd, — но защита от пустого пути оставлена:
// молча писать журнал в "/" было бы хуже, чем не писать его совсем.
func InitLogging() {
	if LogPath() == "" {
		return
	}
	logInitMu.Lock()
	defer logInitMu.Unlock()

	os.MkdirAll(filepath.Dir(LogPath()), 0755)
	rotateIfLargeLocked()

	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "wgkeybot"); err == nil {
		logSyslog = w
	}
	openLogLocked()
}

func openLogLocked() {
	if logF != nil {
		logF.Close()
		logF = nil
	}
	writers := []io.Writer{os.Stderr}
	if f, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		logF = f
		writers = append(writers, f)
	}
	if logSyslog != nil {
		writers = append(writers, logSyslog)
	}
	log.SetOutput(io.MultiWriter(writers...))
	proxy.SetLogFilePath(LogPath())
}

// RotateLogs проверяет размер журнала и при превышении порога ротирует его,
// переоткрывая файловые дескрипторы демона и прокси. Дёргается периодически
// из главного цикла демона.
func RotateLogs() {
	if LogPath() == "" {
		return
	}
	logInitMu.Lock()
	defer logInitMu.Unlock()
	if rotateIfLargeLocked() {
		openLogLocked()
	}
}

func rotateIfLargeLocked() bool {
	st, err := os.Stat(LogPath())
	if err != nil || st.Size() < logMaxBytes {
		return false
	}
	os.Remove(LogPath() + ".0")
	return os.Rename(LogPath(), LogPath()+".0") == nil
}
