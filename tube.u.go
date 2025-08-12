/*
history:
20/0410 v1
20/1106 migrate to github.com/kkdai/youtube/v2

GoGet GoFmt GoBuildNull
GoBuild GoRun
*/

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	yt "github.com/kkdai/youtube/v2"
)

const (
	NL = "\n"
)

var (
	DownloadVideo bool

	FilePerm   os.FileMode = 0644
	Ctx        context.Context
	HttpClient = &http.Client{}
	YtCl       yt.Client
	replacer   *strings.Replacer

	youtubeRe, youtubeListRe *regexp.Regexp

	YoutubeREString     = `(?:youtube.com/watch\?v=|youtu.be/|youtube.com/watch/|youtube.com/shorts/|youtube.com/live/)([0-9A-Za-z_-]+)`
	YoutubeListReString = `(?:youtube.com|youtu.be)/.*[?&]list=([0-9A-Za-z_-]+)$`

	ConfigPath = "$HOME/config/youtube.keys"
	YtKey      string

	YtMaxResults = 50
	TitleMaxLen  = 70
)

type YtPlaylistItemSnippet struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	PublishedAt string `json:"publishedAt"`
	Thumbnails  struct {
		Medium struct {
			Url string `json:"url"`
		} `json:"medium"`
		High struct {
			Url string `json:"url"`
		} `json:"high"`
		Standard struct {
			Url string `json:"url"`
		} `json:"standard"`
		MaxRes struct {
			Url string `json:"url"`
		} `json:"maxres"`
	} `json:"thumbnails"`
	Position   int64 `json:"position"`
	ResourceId struct {
		VideoId string `json:"videoId"`
	} `json:"resourceId"`
}

type YtPlaylistItem struct {
	Snippet YtPlaylistItemSnippet `json:"snippet"`
}

type YtPlaylistItems struct {
	NextPageToken string `json:"nextPageToken"`
	PageInfo      struct {
		TotalResults   int64 `json:"totalResults"`
		ResultsPerPage int64 `json:"resultsPerPage"`
	} `json:"pageInfo"`
	Items []YtPlaylistItem
}

func init() {
	Ctx = context.TODO()
	YtCl = yt.Client{HTTPClient: &http.Client{}}
	youtubeRe = regexp.MustCompile(YoutubeREString)
	youtubeListRe = regexp.MustCompile(YoutubeListReString)

	if path.Base(os.Args[0]) == "tube.v" {
		DownloadVideo = true
	}

	if os.Getenv("YtKey") != "" {
		YtKey = os.Getenv("YtKey")
	}

	if YtKey == "" {
		ConfigPath = os.ExpandEnv(ConfigPath)
		configBb, err := ioutil.ReadFile(ConfigPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reading %s: %v", ConfigPath, err)
		}

		for _, configLine := range strings.Split(string(configBb), "\n") {
			configLine = strings.TrimSpace(configLine)
			if configLine == "" || strings.HasPrefix(configLine, "#") {
				continue
			}

			kv := strings.Split(configLine, "=")
			if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" {
				fmt.Fprintf(os.Stderr, "invalid %s config line: %s\n", ConfigPath, configLine)
				continue
			}

			k, v := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
			v = strings.Trim(v, `'"`)
			if k == "YtKey" {
				YtKey = v
			}
		}
	}

	if YtKey == "" {
		fmt.Fprintln(os.Stderr, "No YtKey provided")
		os.Exit(1)
	}
}

func getJson(url string, target interface{}) error {
	r, err := HttpClient.Get(url)
	if err != nil {
		return err
	}
	defer r.Body.Close()

	return json.NewDecoder(r.Body).Decode(target)
}

func safestring(s string) (t string) {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			r = '.'
		}
		t = t + string(r)
	}

	for strings.Contains(t, "...") {
		t = strings.ReplaceAll(t, "...", "..")
	}

	t = strings.TrimLeft(t, ".")
	t = strings.TrimRight(t, ".")

	if len([]rune(t)) > TitleMaxLen {
		t = string([]rune(t)[:TitleMaxLen])
	}

	return t
}

type YtVideo struct {
	Id         string
	NamePrefix string
}

func main() {
	var err error
	var ytid string
	var NamePrefix string
	var videos []YtVideo

	if len(os.Args) > 2 {
		NamePrefix = os.Args[2]
	}

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: tube.u youtube.id [output.file.name.prefix]\nyoutube.id can be a youtube video or playlist url\n")
		os.Exit(1)
	}
	ytid = os.Args[1]

	var mm []string
	if mm = youtubeListRe.FindStringSubmatch(ytid); len(mm) > 1 {
		videos, err = getList(mm[1], NamePrefix)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	} else if mm = youtubeRe.FindStringSubmatch(ytid); len(mm) > 1 {
		videos = []YtVideo{YtVideo{Id: mm[1], NamePrefix: NamePrefix}}
	}

	var hadErrors bool

	for _, i := range videos {
		err = ytDownToFile(i.Id, i.NamePrefix)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			hadErrors = true
			continue
		}
	}

	if hadErrors {
		os.Exit(1)
	}

	return
}

func getList(ytlistid, nameprefix string) (ytitems []YtVideo, err error) {
	var videos []YtPlaylistItemSnippet
	nextPageToken := ""

	for nextPageToken != "" || len(videos) == 0 {
		var PlaylistItemsUrl = fmt.Sprintf("https://www.googleapis.com/youtube/v3/playlistItems?maxResults=%d&part=snippet&playlistId=%s&key=%s&pageToken=%s", YtMaxResults, ytlistid, YtKey, nextPageToken)

		var playlistItems YtPlaylistItems
		err = getJson(PlaylistItemsUrl, &playlistItems)
		if err != nil {
			return nil, err
		}

		if playlistItems.NextPageToken != nextPageToken {
			nextPageToken = playlistItems.NextPageToken
		} else {
			nextPageToken = ""
		}

		for _, i := range playlistItems.Items {
			videos = append(videos, i.Snippet)
		}
	}

	sort.Slice(videos, func(i, j int) bool { return videos[i].PublishedAt < videos[j].PublishedAt })
	counterlen := int(math.Log10(float64(len(videos)))) + 1
	numfmt := "%0" + strconv.Itoa(counterlen) + "d."

	for vidnum, vid := range videos {
		ytitems = append(
			ytitems,
			YtVideo{Id: vid.ResourceId.VideoId, NamePrefix: nameprefix + fmt.Sprintf(numfmt, vidnum+1)},
		)
	}

	return ytitems, nil
}

func ytDownToFile(ytid, fpath string) error {
	var err error

	vinfo, err := YtCl.GetVideoContext(Ctx, ytid)
	if err != nil {
		return fmt.Errorf("GetVideoContext: %v", err)
	}

	var title string
	if len([]rune(vinfo.Title)) > TitleMaxLen {
		title = string([]rune(vinfo.Title)[:TitleMaxLen])
	} else {
		title = vinfo.Title
	}
	title = safestring(title)

	//fmt.Fprintln(os.Stderr, vinfo.Description)

	var streamfext string
	var streamfmt yt.Format
	if DownloadVideo {
		for _, f := range vinfo.Formats {
			if !strings.HasPrefix(f.MimeType, "video/mp4") {
				continue
			}
			if !strings.HasPrefix(f.MimeType, `video/mp4; codecs="avc1.`) {
				continue
			}
			//if f.AudioQuality == "" {
			//	continue
			//}
			if f.Bitrate > streamfmt.Bitrate {
				//fmt.Fprintf(os.Stderr, "checking stream itag=%v \n", f.ItagNo)
				//if _, streamsize, err := YtCl.GetStreamContext(Ctx, vinfo, &f); err == nil && streamsize > 0 {
				streamfext = fmt.Sprintf("%s..mp4", f.QualityLabel)
				streamfmt = f
				//} else {
				//	fmt.Fprintf(os.Stderr, "streamsize:%d  err:%s"+NL, streamsize, err)
				//}
			}
		}
	} else {
		for _, f := range vinfo.Formats {
			if !strings.HasPrefix(f.MimeType, "audio/") {
				continue
			}
			//fmt.Fprintf(os.Stderr, "mimetype: %s"+NL, f.MimeType)
			var fext string
			if strings.HasPrefix(f.MimeType, "audio/mp4") {
				fext = "m4a"
			} else if strings.HasPrefix(f.MimeType, "audio/mp3") {
				fext = "mp3"
			} else {
				continue
			}
			if f.Bitrate > streamfmt.Bitrate {
				//fmt.Fprintf(os.Stderr, "checking stream itag=%v \n", f.ItagNo)
				//if _, streamsize, err := YtCl.GetStreamContext(Ctx, vinfo, &streamfmt); err == nil && streamsize > 0 {
				streamfext = fext
				streamfmt = f
				//} else {
				//	fmt.Fprintf(os.Stderr, "streamsize:%d  err:%s"+NL, streamsize, err)
				//}
			}
		}
	}

	if streamfmt.ItagNo == 0 {
		return fmt.Errorf("could not find a stream for downloading")
	}

	fpath = fmt.Sprintf("%s%s..%s", fpath, title, streamfext)

	fmt.Fprintln(os.Stderr, fpath)

	if fstat, err := os.Stat(fpath); !os.IsNotExist(err) && fstat.Size() > 0 {
		// the file is there already
		return nil
	}

	streamrc, streamsize, err := YtCl.GetStreamContext(Ctx, vinfo, &streamfmt)
	//fmt.Fprintf(os.Stderr, "downloading stream itag=%v mimetype=%s size=%v err=%v\n", streamfmt.ItagNo, streamfmt.MimeType, streamsize, err)
	if err != nil {
		return err
	}
	if streamsize == 0 {
		return fmt.Errorf("stream size is zero")
	}
	defer streamrc.Close()

	var streambuf bytes.Buffer
	defer streambuf.Reset()
	_, err = io.Copy(&streambuf, streamrc)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(
		fpath,
		streambuf.Bytes(),
		FilePerm,
	)
	if err != nil {
		return err
	}

	return nil
}
