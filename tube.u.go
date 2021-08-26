/*
history:
20/0410 v1
20/1106 migrate to github.com/kkdai/youtube

go get -u -v github.com/kkdai/youtube/...

GoFmt GoBuildNull GoBuild GoRelease GoRun
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	yt "github.com/kkdai/youtube/v2"
)

var (
	FilePerm   os.FileMode = 0644
	Ctx        context.Context
	HttpClient = &http.Client{}
	YtCl       yt.Client
	replacer   *strings.Replacer

	youtubeRe, youtuRe, ytListRe *regexp.Regexp

	YoutubeREString = `youtube.com/watch\?v=([0-9A-Za-z_-]+)`
	YoutuREString   = `youtu.be/([0-9A-Za-z_-]+)$`
	YtListReString  = `youtube.com/.*[?&]list=([0-9A-Za-z_-]+)$`

	ConfigPath = "$HOME/config/youtube.keys"
	YtKey      string

	YtMaxResults = 50
	TitleMaxLen  = 50
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
	youtuRe = regexp.MustCompile(YoutuREString)
	ytListRe = regexp.MustCompile(YtListReString)

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
	if mm = ytListRe.FindStringSubmatch(ytid); len(mm) > 1 {
		videos, err = getList(mm[1], NamePrefix)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	} else if mm = youtubeRe.FindStringSubmatch(ytid); len(mm) > 1 {
		videos = []YtVideo{YtVideo{Id: mm[1], NamePrefix: NamePrefix}}
	} else if mm = youtuRe.FindStringSubmatch(ytid); len(mm) > 1 {
		videos = []YtVideo{YtVideo{Id: mm[1], NamePrefix: NamePrefix}}
	}

	for _, i := range videos {
		err = ytDownToFile(i.Id, i.NamePrefix)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
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

func ytDownToFile(ytid, aupath string) error {
	var err error

	vinfo, err := YtCl.GetVideoContext(Ctx, ytid)
	if err != nil {
		return err
	}

	var title string
	if len([]rune(vinfo.Title)) > TitleMaxLen {
		title = string([]rune(vinfo.Title)[:TitleMaxLen])
	} else {
		title = vinfo.Title
	}
	title = safestring(title)

	aupath = fmt.Sprintf("%s%s.m4a", aupath, title)

	fmt.Fprintln(os.Stderr, aupath)

	if _, err = os.Stat(aupath); !os.IsNotExist(err) {
		return nil
	}

	//fmt.Fprintln(os.Stderr, vinfo.Description)

	var aufmt yt.Format
	for _, f := range vinfo.Formats {
		if !strings.HasPrefix(f.MimeType, "audio/mp4") {
			continue
		}
		if f.Bitrate > aufmt.Bitrate {
			aufmt = f
		}
	}

	streamrc, _, err := YtCl.GetStreamContext(Ctx, vinfo, &aufmt)
	if err != nil {
		return err
	}
	defer streamrc.Close()

	var aubuf bytes.Buffer
	_, err = io.Copy(&aubuf, streamrc)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(
		aupath,
		aubuf.Bytes(),
		FilePerm,
	)
	if err != nil {
		return err
	}

	return nil
}
