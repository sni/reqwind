package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"reqwind/pkg/reqwind"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := reqwind.Run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "reqwind:", err)
		os.Exit(1)
	}
}
