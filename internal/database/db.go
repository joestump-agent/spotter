package database

import (
	"context"
	"fmt"

	"spotter/ent"

	_ "github.com/mattn/go-sqlite3"
)

func NewClient(driver, source string) (*ent.Client, error) {
	client, err := ent.Open(driver, source)
	if err != nil {
		return nil, fmt.Errorf("failed opening connection to %s: %v", driver, err)
	}

	// Run the auto migration tool.
	if err := client.Schema.Create(context.Background()); err != nil {
		client.Close()
		return nil, fmt.Errorf("failed creating schema resources: %v", err)
	}

	return client, nil
}
