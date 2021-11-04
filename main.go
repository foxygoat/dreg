package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"foxygo.at/dreg/pb"
	"foxygo.at/protog/httprule"
	"github.com/alecthomas/kong"
	"github.com/dustin/go-humanize"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type config struct {
	Check check `cmd:"" help:"Check that registry supports V2 API"`
	List  list  `cmd:"" help:"List images in registry"`
	Rm    rm    `cmd:"" aliases:"rmi" help:"Remove images from registry"`

	DockerConfig string `type:"path" default:"~/.docker/config.json" help:"Path to docker config file for auth creds"`
	URL          string `default:"http://localhost:5000" env:"REGISTRY" help:"URL of registry"`
	Verbose      bool   `short:"v" help:"Verbose output"`

	client pb.RegistryClient
	dcfg   dockerConfig
}

type check struct{}

type list struct {
	Repositories []string `arg:"" optional:"" name:"repository" help:"Repositories to list"`
	Sizes        bool     `short:"s" help:"Show image sizes (slow)"`
	Table        bool     `help:"Show output as a table"`
}

type rm struct {
	Images []string `arg:"" name:"image" help:"Images to delete from registry"`
}

// dockerConfig matches the structure of the docker config.json file, with just
// the elements we are interested in.
type dockerConfig struct {
	Auths map[string]struct{ Auth string }
}

func main() {
	c := config{}
	if err := kong.Parse(&c).Run(&c); err != nil {
		handleError(err)
	}
}

func handleError(err error) {
	st, ok := status.FromError(err)
	if !ok {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch st.Code() {
	case codes.Unauthenticated, codes.PermissionDenied:
		fmt.Fprintln(os.Stderr, "Unauthenticated. Login with 'docker login'")
	default:
		fmt.Fprintln(os.Stderr, err)
	}
}

func (c *config) AfterApply() error {
	// Read docker config for auth tokens. Ignore errors - the file is optional
	if f, err := os.Open(c.DockerConfig); err == nil {
		_ = json.NewDecoder(f).Decode(&c.dcfg)
		f.Close()
	}

	opts := []httprule.Option{}
	if opt := authOpt(c.dcfg, c.URL); opt != nil {
		opts = append(opts, opt)
	}

	cc := httprule.NewClientConn(c.URL, opts...)
	c.client = pb.NewRegistryClient(cc)

	return nil
}

func authOpt(dcfg dockerConfig, regURL string) httprule.Option {
	u, err := url.Parse(regURL)
	if err != nil {
		return nil
	}
	// Only use auth on insecure http if localhost (loopback)
	if u.Scheme == "http" {
		ips, err := net.LookupIP(u.Host)
		if err != nil {
			return nil
		}
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return nil
			}
		}
	}
	token, ok := dcfg.Auths[u.Host]
	if !ok {
		return nil
	}
	return httprule.WithHeader("Authorization", "Basic "+token.Auth)
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

// list.Run executes the list cli subcommand, listing the images in a registry.
func (l *list) Run(cfg *config) error {
	ctx := context.Background()
	if len(l.Repositories) == 0 {
		req := &pb.ListRepositoriesRequest{}
		resp, err := cfg.client.ListRepositories(ctx, req)
		if err != nil {
			return err
		}
		l.Repositories = resp.Repositories
		sort.Strings(l.Repositories)
	}

	sep := ':'
	var w io.Writer = os.Stdout
	if l.Table {
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		defer tw.Flush()
		w = tw
		heading := "REPOSITORY\tTAG"
		if l.Sizes {
			heading += "\tSIZE"
		}
		fmt.Fprintln(w, heading)
		sep = '\t'
	}

	for _, name := range l.Repositories {
		req := &pb.ListImageTagsRequest{Name: name}
		resp, err := cfg.client.ListImageTags(ctx, req)
		if err != nil {
			return err
		}

		sort.Strings(resp.Tags)
		for _, tag := range resp.Tags {
			if !l.Sizes {
				fmt.Fprintf(w, "%s%c%s\n", name, sep, tag)
				continue
			}
			req := &pb.GetManifestRequest{Name: name, Reference: tag}
			resp, err := cfg.client.GetManifest(ctx, req)
			if err != nil {
				return err
			}
			var size uint64
			for _, layer := range resp.Manifest.Layers {
				size += layer.Size
			}
			fmt.Fprintf(w, "%s%c%s\t%s\n", name, sep, tag, humanize.Bytes(size)+" (compressed)")
		}
	}
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
