package main

import (
	"context"
	"fmt"
	"net/http"

	"foxygo.at/dreg/dockerpb"
	"foxygo.at/protog/httprule"
	"github.com/alecthomas/kong"
)

type config struct {
	Check check `cmd:"" help:"Check that registry supports V2 API"`
	List  list  `cmd:"" help:"List images in registry"`

	URL string `default:"http://localhost:5000" env:"REGISTRY" help:"URL of registry"`

	client dockerpb.RegistryClient
}

func (c *config) AfterApply() error {
	c.client = dockerpb.NewRegistryClient(&httprule.ClientConn{
		HTTPClient: &http.Client{},
		BaseURL:    c.URL,
	})
	return nil
}

type check struct{}

func (c *check) Run(cfg *config) error {
	ctx := context.Background()
	req := &dockerpb.CheckV2Request{}
	if _, err := cfg.client.CheckV2(ctx, req); err != nil {
		return err
	}

	fmt.Println("OK")
	return nil
}

type list struct {
	Repositories []string `arg:"" optional:"" name:"repository" help:"Repositories to list"`
}

func (l *list) Run(cfg *config) error {
	ctx := context.Background()
	req := &dockerpb.ListRepositoriesRequest{}
	if len(l.Repositories) == 0 {
		resp, err := cfg.client.ListRepositories(ctx, req)
		if err != nil {
			return err
		}
		l.Repositories = resp.Repositories
	}

	for _, name := range l.Repositories {
		req := &dockerpb.ListImageTagsRequest{Name: name}
		resp, err := cfg.client.ListImageTags(ctx, req)
		if err != nil {
			return err
		}
		for _, tag := range resp.Tags {
			fmt.Printf("%s:%s\n", resp.Name, tag)
		}
	}
	return nil
}

func main() {
	c := config{}
	kctx := kong.Parse(&c)
	err := kctx.Run(&c)
	kctx.FatalIfErrorf(err)
}
