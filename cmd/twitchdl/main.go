package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	twitchdl "github.com/jybp/twitch-downloader"
	"github.com/jybp/twitch-downloader/twitch"
)

// Must be injected at build time using the -ldflags flag.
// go build -ldflags "-X main.defaultClientID=YourClientID"

// go build -ldflags "-X main.defaultClientID=kimne78kx3ncx6brgo4mv6wki5h1ko"
var defaultClientID string



// Flags 

// command line flags like e.g -vod, -start when running 
// flag.typeVar(&flagvar, "flagName", "default value", "help messsage of r flag name")

var clientID, vodID, quality, output string
var start, end time.Duration

func init() {
	log.SetFlags(0)

	flag.StringVar(&vodID, "vod", "", `The ID or absolute URL of the twitch VOD to download. https://www.twitch.tv/videos/12345 is the VOD with ID "12345".`)
	flag.StringVar(&quality, "q", "", "Quality of the VOD to download. Omit this flag to print the available qualities.")
	flag.StringVar(&output, "o", "", `Path where the VOD will be downloaded. (optional)`)
	flag.DurationVar(&start, "start", time.Duration(0), "Specify \"start\" to download a subset of the VOD. Example: 1h23m45s (optional)")
	flag.DurationVar(&end, "end", time.Duration(0), "Specify \"end\" to download a subset of the VOD. Example: 1h34m56s (optional)")
	flag.StringVar(&clientID, "client-id", "", "Use a specific twitch.tv API client ID. (optional)")
	flag.Parse()
}

func main() {

	isClip := false
	if len(clientID) > 0 {
		defaultClientID = clientID
	}

	if len(defaultClientID) == 0 {
		panic("no default client id specified")
	}
	
	if len(vodID) == 0 {
		flag.PrintDefaults()
		return
	}

	id, err := twitch.ID(vodID)
	if err == nil {
		vodID = id
	} else{
		// check if its clip url instead
		//fmt.Println(err)
		id, err := twitch.ID_Clip(vodID)
		if err == nil {
			vodID = id
			isClip = true
			//fmt.Println(id)
		} 
	}
	
	var vod twitch.VOD
	api := twitch.New(http.DefaultClient, defaultClientID)
	//fmt.Println(api)
	if isClip{
		vod, err = api.Clip(context.Background(), vodID)
	} else{
		vod, err = api.VOD(context.Background(), vodID)
	}

	if err != nil {
		if isClip{
			log.Fatalf("Retrieving video informations for Clip %s failed: %v", vodID, err)
		} else{
			log.Fatalf("Retrieving video informations for VOD %s failed: %v", vodID, err)
		}
	}

	if len(quality) == 0 {
		var qualities []string
		if isClip{
			qualities, err = twitchdl.Qualities_clip(context.Background(), http.DefaultClient, defaultClientID, vodID)
		} else{
			qualities, err = twitchdl.Qualities(context.Background(), http.DefaultClient, defaultClientID, vodID)
		}

		if err != nil {
			if isClip{
				log.Fatalf("Retrieving qualities for Clip %s failed: %v", vodID, err)
			} else{
				log.Fatalf("Retrieving qualities for VOD %s failed: %v", vodID, err)
			}
		}
		fmt.Printf("%s\n%s\n", vod.Title, strings.Join(qualities, "\n"))
		return
	}

	var download *twitchdl.Merger
	if isClip{
		download, err = twitchdl.Download_clip(context.Background(), http.DefaultClient, defaultClientID, vodID, quality)
		if err != nil {
			log.Fatalf("Retrieving stream for Clip %s failed: %v", vodID, err)
		}
	} else{

		download, err = twitchdl.Download(context.Background(), http.DefaultClient, defaultClientID, vodID, quality, start, end)
		if err != nil {
			log.Fatalf("Retrieving stream for VOD %s failed: %v", vodID, err)
		}
	}

	path, filename := filepath.Split(output)
	if len(filename) == 0 {
		ext := "mp4"
		if strings.Contains(strings.ToLower(quality), "audio") {
			ext = "mp4a"
		}
		filename = fmt.Sprintf("%s (%s).%s", vod.Title, quality, ext)
	}
	output = filepath.Join(path, filename)

	f, err := os.OpenFile(output, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		log.Fatalf("Cannot create file %s: %v", output, err)
	}

	fmt.Printf("Downloading: %s\n", f.Name())

	if _, err := io.Copy(f, &reader{r: download}); err != nil {
		log.Fatalf("Writing to file %s failed: %v", output, err)
	}
	if err := f.Close(); err != nil {
		log.Fatalf("Closing file %s failed: %v", output, err)
	}
	fmt.Printf("\rDone%-25s\n", " ")
}

// reader prints the download progress every second.
type reader struct {
	r *twitchdl.Merger

	from time.Time
	n    uint64
	t    uint64
}

func (r *reader) Read(p []byte) (n int, err error) {
	n, err = r.r.Read(p)
	r.n += uint64(n)
	r.t += uint64(n)
	if time.Now().Sub(r.from) > time.Second {
		progress := float64(r.r.Current()) * 100 / float64(r.r.Chunks())
		fmt.Printf("\r%-12s %-10s %-2d%%",
			r.btos(r.bitrate())+"/s",
			r.btos(r.t),
			int(math.Round(progress)))
		r.from = time.Now()
		r.n = 0
	}
	return
}

func (*reader) btos(b uint64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(u), 0
	for n := b / u; n >= u; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (r *reader) bitrate() uint64 {
	return uint64(r.n) * uint64(time.Second) / uint64(time.Now().Sub(r.from))
}
