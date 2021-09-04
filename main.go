package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"foxygo.at/dreg/pb"
	"foxygo.at/protog/httprule"
	"github.com/alecthomas/kong"
	"github.com/dustin/go-humanize"
)

type config struct {
	Check check `cmd:"" help:"Check that registry supports V2 API"`
	List  list  `cmd:"" help:"List images in registry"`
	Rm    rm    `cmd:"" aliases:"rmi" help:"Remove images from registry"`

	URL     string `default:"http://localhost:5000" env:"REGISTRY" help:"URL of registry"`
	Verbose bool   `short:"v" help:"Verbose output"`

	client pb.RegistryClient
}

type check struct{}

type list struct {
	Repositories []string `arg:"" optional:"" name:"repository" help:"Repositories to list"`
}

type rm struct {
	Images []string `arg:"" name:"image" help:"Images to delete from registry"`
}

func main() {
	c := config{}
	kctx := kong.Parse(&c)
	err := kctx.Run(&c)
	kctx.FatalIfErrorf(err)
}

func (c *config) AfterApply() error {
	c.client = pb.NewRegistryClient(&httprule.ClientConn{
		HTTPClient: &http.Client{},
		BaseURL:    c.URL,
	})
	return nil
}

func (c *check) Run(cfg *config) error {
	ctx := context.Background()
	req := &pb.CheckV2Request{}
	if _, err := cfg.client.CheckV2(ctx, req); err != nil {
		return err
	}

	fmt.Println("OK")
	return nil
}

func (l *list) Run(cfg *config) error {
	ctx := context.Background()
	req := &pb.ListRepositoriesRequest{}
	if len(l.Repositories) == 0 {
		resp, err := cfg.client.ListRepositories(ctx, req)
		if err != nil {
			return err
		}
		l.Repositories = resp.Repositories
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY\tTAG\tSIZE")

	for _, name := range l.Repositories {
		req := &pb.ListImageTagsRequest{Name: name}
		resp, err := cfg.client.ListImageTags(ctx, req)
		if err != nil {
			return err
		}
		for _, tag := range resp.Tags {
			req := &pb.GetManifestRequest{Name: name, Reference: tag}
			resp, err := cfg.client.GetManifest(ctx, req)
			if err != nil {
				return err
			}
			var size uint64
			for _, layer := range resp.Manifest.Layers {
				size += layer.Size
			}
			fields := []string{name, tag, humanize.Bytes(size) + " (compressed)"}
			line := strings.Join(fields, "\t")
			fmt.Fprintln(w, line)
		}
	}
	w.Flush()
	return nil
}

func (r *rm) Run(cfg *config) error {
	ctx := context.Background()
	n := 0
	for _, image := range r.Images {
		if !strings.Contains(image, ":") {
			image += ":latest"
		}
		parts := strings.SplitN(image, ":", 2)
		req := &pb.GetDigestRequest{Name: parts[0], Reference: parts[1]}
		resp, err := cfg.client.GetDigest(ctx, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Couldn't find %s: %v\n", image, err)
			continue
		}
		delReq := &pb.DeleteImageRequest{Name: parts[0], Reference: resp.Digest}
		_, err = cfg.client.DeleteImage(ctx, delReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Couldn't remove %s: %v\n", image, err)
			continue
		}
		if cfg.Verbose {
			fmt.Printf("%s removed\n", image)
		}
		n++
	}

	if n != len(r.Images) {
		return fmt.Errorf("%d image(s) not removed", len(r.Images)-n)
	}
	return nil
}
