package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/DenisHumen/krokosha-site/api/internal/indexnow"
)

// runIndexNow tells search engines which pages of a fresh release changed (brief B7).
// deploy/bin/build-release.sh calls it right after the release went live; the key and the site
// come from /etc/krokosha/env through the environment of krokosha-sync.service.
//
//	krokosha-cli indexnow --release /var/www/krokosha/releases/20260922-041500 --previous …/20260921-221500
func runIndexNow(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("indexnow", flag.ContinueOnError)
	site := flags.String("site", os.Getenv("SITE_URL"), "the public origin of the site (default: SITE_URL)")
	key := flags.String("key", os.Getenv("INDEXNOW_KEY"), "the IndexNow key, served as /<key>.txt (default: INDEXNOW_KEY)")
	endpoint := flags.String("endpoint", os.Getenv("INDEXNOW_API"), "where to submit (default: INDEXNOW_API, else "+indexnow.DefaultEndpoint+")")
	releaseDir := flags.String("release", "", "the directory of the release that just went live")
	previous := flags.String("previous", "", "the directory of the release before it: only pages that differ are submitted")
	all := flags.Bool("all", false, "submit every page of the sitemap")
	flags.Usage = func() {
		_, _ = fmt.Fprint(flags.Output(), "Usage: krokosha-cli indexnow --release DIR [--previous DIR] [--all]\n\nTells Bing, Yandex and the other engines of IndexNow which pages changed. Nothing is sent when nothing did.\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *releaseDir == "" {
		flags.Usage()
		return errors.New("--release is required")
	}
	if *key == "" {
		fmt.Println("IndexNow: no key (INDEXNOW_KEY), nothing to do")
		return nil
	}
	result, err := indexnow.Submit(ctx, indexnow.Options{
		Site: *site, Key: *key, Endpoint: *endpoint, Release: *releaseDir, Previous: *previous, All: *all,
	})
	if err != nil {
		return fmt.Errorf("IndexNow: %w", err)
	}
	if len(result.Changed) == 0 {
		fmt.Printf("IndexNow: none of the %d pages changed, nothing to tell\n", len(result.Pages))
		return nil
	}
	paths := make([]string, 0, len(result.Changed))
	for _, page := range result.Changed {
		if parsed, err := url.Parse(page); err == nil {
			paths = append(paths, parsed.Path)
		}
	}
	where := *endpoint
	if where == "" {
		where = indexnow.DefaultEndpoint
	}
	if parsed, err := url.Parse(where); err == nil && parsed.Host != "" {
		where = parsed.Host
	}
	fmt.Printf("IndexNow: told %s about %d of %d pages: %s\n", where, len(result.Changed), len(result.Pages), strings.Join(paths, " "))
	return nil
}
