package twitchdl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
	//"net/http/httputil"
	"github.com/jybp/twitch-downloader/m3u8"
	"github.com/jybp/twitch-downloader/twitch"
	"github.com/pkg/errors"
)


// Qualities return the qualities available for the Clip "vodID".
func Qualities_clip(ctx context.Context, client *http.Client, clientID, vodID string) ([]string, error) {
	var qualities []string
	api := twitch.New(client, clientID)
	clip_info,err :=api.Clip_url(context.Background(), vodID)
	if err != nil {
		return nil, err
	}

	for _,v:= range clip_info{
		qualities = append(qualities,v.Quality_option)
	}

	return qualities, nil
}


// Download sets up the download of the Clip "vodId" with quality "quality"
// using the provided http.Client.
// The download is actually perfomed when the returned io.Reader is being read.
func Download_clip(ctx context.Context, client *http.Client, clientID, vodID, quality string) (r *Merger, err error) {
	api := twitch.New(client, clientID)
	clip_info,err :=api.Clip_url(context.Background(), vodID)
	if err != nil {
		return nil, err
	}
	var variant *twitch.Clip
	for _,v:= range clip_info{
		if v.Quality_option != quality {
			continue
		}
		variant = &v
		break
	}

	if variant == nil {
		return nil, errors.Errorf("quality %s not found", quality)
	}

	tok, sig, err := api.ClipToken(ctx, vodID)
	if err != nil {
		return nil, errors.Errorf("something went wrong getting authenticated clip url [%s]: \n%v", variant.SourceURL, err)
	}

	auth_source_url := fmt.Sprintf("%s?sig=%s&token=%s", variant.SourceURL, sig, tok)

	req, err := http.NewRequest(http.MethodGet, auth_source_url, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var downloadFns []downloadFunc
	downloadFns = append(downloadFns, prepare(client, req))

	return &Merger{downloads: downloadFns}, nil
}


// Qualities return the qualities available for the VOD "vodID".
func Qualities(ctx context.Context, client *http.Client, clientID, vodID string) ([]string, error) {
	api := twitch.New(client, clientID)
	m3u8raw, err := api.M3U8(ctx, vodID)
	if err != nil {
		return nil, err
	}
	master, err := m3u8.Master(bytes.NewReader(m3u8raw))
	if err != nil {
		return nil, err
	}
	var qualities []string
	for _, variant := range master.Variants {
		for _, alt := range variant.Alternatives {
			qualities = append(qualities, alt.Name)
		}
	}
	return qualities, nil
}

// Download sets up the download of the VOD "vodId" with quality "quality"
// using the provided http.Client.
// The download is actually perfomed when the returned io.Reader is being read.
func Download(ctx context.Context, client *http.Client, clientID, vodID, quality string, start, end time.Duration) (r *Merger, err error) {
	api := twitch.New(client, clientID)
	m3u8raw, err := api.M3U8(ctx, vodID)
	if err != nil {
		return nil, err
	}
	master, err := m3u8.Master(bytes.NewReader(m3u8raw))
	if err != nil {
		return nil, err
	}

	var variant m3u8.Variant
L:
	for _, v := range master.Variants {
		for _, alt := range v.Alternatives {
			if alt.Name != quality {
				continue
			}
			variant = v
			break L
		}
	}

	if len(variant.URL) == 0 {
		return nil, errors.Errorf("quality %s not found", quality)
	}

	mediaResp, err := client.Get(variant.URL)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer mediaResp.Body.Close()
	media, err := m3u8.Media(mediaResp.Body, variant.URL)
	if err != nil {
		return nil, err
	}

	var downloadFns []downloadFunc
	segments, err := sliceSegments(media.Segments, start, end)
	if err != nil {
		return nil, err
	}
	for _, segment := range segments {
		req, err := http.NewRequest(http.MethodGet, segment.URL, nil)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		downloadFns = append(downloadFns, prepare(client, req))
	}

	return &Merger{downloads: downloadFns}, nil
}

func sliceSegments(segments []m3u8.MediaSegment, start, end time.Duration) ([]m3u8.MediaSegment, error) {
	if start < 0 || end < 0 {
		return nil, errors.New("Negative timestamps are not allowed")
	}
	if start >= end && end != time.Duration(0) {
		return nil, errors.New("End timestamp is not after Start timestamp")
	}
	if start == time.Duration(0) && end == time.Duration(0) {
		return segments, nil
	}
	if end == time.Duration(0) {
		end = time.Duration(math.MaxInt64)
	}
	slice := []m3u8.MediaSegment{}
	segmentStart := time.Duration(0)
	for _, segment := range segments {
		segmentEnd := segmentStart + segment.Duration
		if segmentEnd <= start {
			segmentStart += segment.Duration
			continue
		}
		if segmentStart >= end {
			break
		}
		slice = append(slice, segment)
		segmentStart += segment.Duration
	}
	if len(slice) == 0 {
		var dur time.Duration
		for _, segment := range segments {
			dur += segment.Duration
		}
		return nil, fmt.Errorf("Timestamps are not a subset of the video (video duration is %v)", dur)
	}
	return slice, nil
}

// downloadFunc describes a func that peform an HTTP request and returns the response.Body
type downloadFunc func() (io.ReadCloser, error)

func prepare(client *http.Client, req *http.Request) downloadFunc {
	return func() (io.ReadCloser, error) {
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if s := resp.StatusCode; s < 200 || s >= 300 {
			return nil, errors.Errorf("%d: %s", s, req.URL)
		}
		return resp.Body, nil
	}
}

// Merger merges the "downloads" into a single io.Reader.
type Merger struct {
	downloads []downloadFunc

	index   int
	current io.ReadCloser
	err     error
}

func (r *Merger) next() error {
	if r.index >= len(r.downloads) {
		r.current = nil
		r.index++
		return nil
	}
	var err error
	r.current, err = r.downloads[r.index]()
	r.index++
	return err
}

// Read allows Merger to implement io.Reader.
func (r *Merger) Read(p []byte) (int, error) {
	for {
		if r.err != nil {
			return 0, r.err
		}
		if r.current != nil {
			n, err := r.current.Read(p)
			if err == io.EOF {
				err = r.current.Close()
				r.current = nil
			}
			return n, errors.WithStack(err)
		}
		if err := r.next(); err != nil {
			return 0, err
		}
		if r.current == nil {
			return 0, io.EOF
		}
	}
}

// Chunks returns the number of chunks.
func (r *Merger) Chunks() int {
	return len(r.downloads)
}

// Current returns the number of chunks already processed.
func (r *Merger) Current() int {
	return r.index
}
