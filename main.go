package main

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	client, err := NewClient()
	if err != nil {
		log.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "trakt", Version: "0.1.0"}, nil)
	addTools(server, client)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
