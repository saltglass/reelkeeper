// Command fakenews is a made-up Usenet for Reelkeeper's dev stack, so the
// Usenet clients can be tested end to end without a Usenet subscription and
// without the internet. It is never part of Reelkeeper's image.
//
//	fakenews serve   a news server (NNTP, :119) and a Newznab indexer (:8080)
//	fakenews make    make a release: a made-up video plus par2 repair files,
//	                 split into yEnc articles, and its .nzb
//
// Everything lives in the data folder (-data, default /data): releases/<name>/
// holds release.json and the .nzb, articles/ holds one file per article.
//
// To break things on purpose: `make -missing N` deletes N of the video's
// articles (par2 repairs a few; many make the download fail), and
// POST /control?down=1 takes the news server away (down=0 brings it back).
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		data := fs.String("data", "/data", "data folder")
		nntpAddr := fs.String("nntp", ":119", "news server address")
		httpAddr := fs.String("http", ":8080", "indexer address")
		base := fs.String("base", "", "the address .nzb links point at (default: the address each search came to)")
		_ = fs.Parse(os.Args[2:])
		news := &newsServer{data: *data, log: log}
		l, err := net.Listen("tcp", *nntpAddr)
		if err != nil {
			fail(err)
		}
		go func() { fail(news.serve(l)) }()
		log.Info("fakenews ready", "nntp", *nntpAddr, "http", *httpAddr)
		srv := &http.Server{Addr: *httpAddr, Handler: &indexer{data: *data, base: *base, news: news, log: log}, ReadHeaderTimeout: 10 * time.Second}
		fail(srv.ListenAndServe())
	case "make":
		fs := flag.NewFlagSet("make", flag.ExitOnError)
		data := fs.String("data", "/data", "data folder")
		size := fs.Int("size", 20, "the video's size in MB")
		cat := fs.Int("cat", 0, "Newznab category (default: 5000 for names with S01E02, else 2000)")
		missing := fs.Int("missing", 0, "delete this many of the video's articles after posting")
		noPar := fs.Bool("no-par2", false, "leave out the par2 repair files")
		_ = fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			usage()
		}
		r, err := makeRelease(*data, MakeOptions{Name: fs.Arg(0), SizeMB: *size, Category: *cat, Missing: *missing, NoPar2: *noPar})
		if err != nil {
			fail(err)
		}
		n := 0
		for _, f := range r.Files {
			n += len(f.Segments)
		}
		fmt.Printf("made %s: %d files, %d articles, %d missing, %.1f MB\n", r.Name, len(r.Files), n, len(r.Missing), float64(r.Size)/(1<<20))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: fakenews serve [-data DIR] [-nntp :119] [-http :8080] [-base URL]\n       fakenews make [-data DIR] [-size MB] [-cat N] [-missing N] [-no-par2] RELEASE.NAME")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fakenews:", err)
	os.Exit(1)
}
