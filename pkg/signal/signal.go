package signal

import (
    "os"
    "os/signal"
    "syscall"
)

// SetupSignalHandler returns a stop channel that's closed on SIGINT/SIGTERM.
func SetupSignalHandler() <-chan struct{} {
    stop := make(chan struct{})
    signals := make(chan os.Signal, 2)
    signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-signals
        close(stop)
    }()
    return stop
}