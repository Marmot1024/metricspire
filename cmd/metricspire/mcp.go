package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func runMCP(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	origin := flags.String("api-url", "", "MetricSpire HTTPS origin; bearer token from METRICSPIRE_API_TOKEN")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *origin == "" || flags.NArg() != 0 {
		return errors.New("mcp requires --api-url and no positional arguments")
	}
	server, err := mcpbridge.New(*origin, os.Getenv("METRICSPIRE_API_TOKEN"), version)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// stdout is exclusively the MCP wire; diagnostics belong on stderr.
	return server.Run(ctx, &mcp.IOTransport{Reader: os.Stdin, Writer: mcpOutput{stdout}})
}

type mcpOutput struct{ io.Writer }

func (mcpOutput) Close() error { return nil }
