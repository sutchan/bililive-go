package huya

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/pkg/utils"
	"github.com/hr3lxphr6j/requests"
	"github.com/tidwall/gjson"
)

const uaForLol = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

var downloaderHeadersForLol = func() map[string]string {
	headers := getGeneralHeadersForDownloader()
	headers["User-Agent"] = uaForLol
	return headers
}()

func GetInfo_ForLol(l *Live, body string) (info *live.Info, err error) {
	var (
		strFilter = utils.NewStringFilterChain(utils.ParseUnicode, utils.UnescapeHTMLEntity)
		hostName  string
		roomName  string
		status    string
	)

	// 尝试多种正则表达式匹配主播名称
	hostNamePatterns := []string{
		`"nick":"([^"]*)"`,
		`"userName":"([^"]*)"`,
		`"nickName":"([^"]*)"`,
		`"anchorName":"([^"]*)"`,
		`"sNickName":"([^"]*)"`,
	}

	// 尝试多种正则表达式匹配直播间名称
	roomNamePatterns := []string{
		`"introduction":"([^"]*)"`,
		`"roomName":"([^"]*)"`,
		`"title":"([^"]*)"`,
		`"sRoomName":"([^"]*)"`,
	}

	// 尝试多种正则表达式匹配直播状态
	statusPatterns := []string{
		`"isOn":([^,]*),`,
		`"liveStatus":([^,]*),`,
		`"status":([^,]*),`,
	}

	// 匹配主播名称
	for _, pattern := range hostNamePatterns {
		if hostName = strFilter.Do(utils.Match1(pattern, body)); hostName != "" {
			break
		}
	}

	// 匹配直播间名称
	for _, pattern := range roomNamePatterns {
		if roomName = strFilter.Do(utils.Match1(pattern, body)); roomName != "" {
			break
		}
	}

	// 匹配直播状态
	for _, pattern := range statusPatterns {
		if status = strFilter.Do(utils.Match1(pattern, body)); status != "" {
			break
		}
	}

	// 如果基本正则匹配失败，尝试从JSON数据中提取
	if hostName == "" || roomName == "" || status == "" {
		// 尝试从HTML中提取JSON数据
		jsonData := utils.Match1(`window\.HYLiveInfo\s*=\s*(\{.*?\});`, body)
		if jsonData != "" {
			gj := gjson.Parse(jsonData)
			if hostName == "" {
				hostName = gj.Get("nick").String()
				if hostName == "" {
					hostName = gj.Get("anchorInfo.nick").String()
				}
			}
			if roomName == "" {
				roomName = gj.Get("roomName").String()
				if roomName == "" {
					roomName = gj.Get("roomInfo.roomName").String()
				}
			}
			if status == "" {
				status = strconv.FormatBool(gj.Get("liveStatus").Int() == 1)
				if status == "" {
					status = strconv.FormatBool(gj.Get("isLiving").Bool())
				}
			}
		}
	}

	if hostName == "" || roomName == "" || status == "" {
		// 尝试从另一个可能的JSON位置提取
		jsonData2 := utils.Match1(`window\.INIT_DATA\s*=\s*(\{.*?\});`, body)
		if jsonData2 != "" {
			gj := gjson.Parse(jsonData2)
			if hostName == "" {
				hostName = gj.Get("anchor.nick").String()
			}
			if roomName == "" {
				roomName = gj.Get("room.roomName").String()
			}
			if status == "" {
				status = strconv.FormatBool(gj.Get("room.liveStatus").Int() == 1)
			}
		}
	}

	// 最后尝试从页面标题中提取直播间名称
	if roomName == "" {
		roomName = strFilter.Do(utils.Match1(`<title>(.*?) - 虎牙直播</title>`, body))
	}

	if hostName == "" || roomName == "" {
		return nil, live.ErrInternalError
	}

	// 确保status有一个合理的默认值
	isLiving := false
	if status == "true" || status == "1" || strings.ToLower(status) == "on" {
		isLiving = true
	}

	info = &live.Info{
		Live:     l,
		HostName: hostName,
		RoomName: roomName,
		Status:   isLiving,
	}
	return info, nil
}

func GetStreamInfos_ForLol(l *Live) (infos []*live.StreamUrlInfo, err error) {
	resp, err := l.RequestSession.Get(l.Url.String(), requests.UserAgent(uaForLol))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status code: %d", resp.StatusCode)
	}
	body, err := resp.Text()
	if err != nil {
		return nil, err
	}

	tmpStrings := strings.Split(body, `stream: `)
	if len(tmpStrings) < 2 {
		return nil, fmt.Errorf("stream json info not found")
	}
	tmpStreamJsonRawString := strings.Split(tmpStrings[1], `};`)
	if len(tmpStreamJsonRawString) < 1 {
		return nil, fmt.Errorf("stream json info end not found. stream text: %s", tmpStrings[1])
	}
	streamJsonRawString := tmpStreamJsonRawString[0]
	if !gjson.Valid(streamJsonRawString) {
		return nil, fmt.Errorf("streamJsonRawString not valid")
	}
	streamJson := gjson.Parse(streamJsonRawString)
	vMultiStreamInfoJson := streamJson.Get("vMultiStreamInfo").Array()
	if len(vMultiStreamInfoJson) == 0 {
		return nil, fmt.Errorf("vMultiStreamInfo not found")
	}

	streamInfoJsons := streamJson.Get("data.0.gameStreamInfoList").Array()
	if len(streamInfoJsons) == 0 {
		return nil, fmt.Errorf("gameStreamInfoList not found")
	}
	index := l.LastCdnIndex
	if index >= len(streamInfoJsons) {
		index = 0
	}
	l.LastCdnIndex = index + 1
	gameStreamInfoJson := streamInfoJsons[index]
	urls, err := getStreamUrlsFromGameStreamInfoJson(gameStreamInfoJson)
	if err != nil {
		return nil, err
	}
	return utils.GenUrlInfos(urls, downloaderHeadersForLol), nil
}

func getStreamUrlsFromGameStreamInfoJson(gameStreamInfoJson gjson.Result) (us []*url.URL, err error) {
	// get streamName
	sStreamName := gameStreamInfoJson.Get("sStreamName").String()
	// get sFlvAntiCode
	sFlvAntiCode := gameStreamInfoJson.Get("sFlvAntiCode").String()
	// get sFlvUrl
	sFlvUrl := gameStreamInfoJson.Get("sFlvUrl").String()
	// get random uid
	uid := rand.Int63n(99999999999) + 1200000000000

	query, err := parseAntiCode(sFlvAntiCode, uid, sStreamName)
	if err != nil {
		return nil, err
	}
	tmpUrlString := fmt.Sprintf("%s/%s.flv?%s", sFlvUrl, sStreamName, query)
	u, err := url.Parse(tmpUrlString)
	if err != nil {
		return nil, err
	}
	return []*url.URL{u}, nil
}

type urlQueryParams struct {
	WsSecret string
	WsTime   string
	Seqid    string
	Ctype    string
	Ver      string
	Fs       string
	U        string
	T        string
	Sv       string
	Sdk_sid  string
	Codec    string
}

func parseAntiCode(anticode string, uid int64, streamName string) (string, error) {
	qr, err := url.ParseQuery(anticode)
	if err != nil {
		return "", err
	}
	resultTemplate := template.Must(template.New("urlQuery").Parse(
		"wsSecret={{.WsSecret}}" +
			"&wsTime={{.WsTime}}" +
			"&seqid={{.Seqid}}" +
			"&ctype={{.Ctype}}" +
			"&ver={{.Ver}}" +
			"&fs={{.Fs}}" +
			"&u={{.U}}" +
			"&t={{.T}}" +
			"&sv={{.Sv}}" +
			"&sdk_sid={{.Sdk_sid}}" +
			"&codec={{.Codec}}",
	))
	timeNow := time.Now().Unix() * 1000
	resultParams := urlQueryParams{
		WsSecret: "",
		WsTime:   qr.Get("wsTime"),
		Seqid:    strconv.FormatInt(timeNow+uid, 10),
		Ctype:    qr.Get("ctype"),
		Ver:      "1",
		Fs:       qr.Get("fs"),
		U:        strconv.FormatInt(uid, 10),
		T:        "100",
		Sv:       "2405220949",
		Sdk_sid:  strconv.FormatInt(uid, 10),
		Codec:    "264",
	}
	ss := getMD5Hash(fmt.Sprintf("%s|%s|%s", resultParams.Seqid, resultParams.Ctype, resultParams.T))

	decodeString, _ := base64.StdEncoding.DecodeString(qr.Get("fm"))
	fm := string(decodeString)
	fm = strings.ReplaceAll(fm, "$0", resultParams.U)
	fm = strings.ReplaceAll(fm, "$1", streamName)
	fm = strings.ReplaceAll(fm, "$2", ss)
	fm = strings.ReplaceAll(fm, "$3", resultParams.WsTime)

	resultParams.WsSecret = getMD5Hash(fm)
	var buf bytes.Buffer
	if err := resultTemplate.Execute(&buf, resultParams); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func getMD5Hash(text string) string {
	hash := md5.Sum([]byte(text))
	return hex.EncodeToString(hash[:])
}
