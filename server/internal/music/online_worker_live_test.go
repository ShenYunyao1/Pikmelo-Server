package music

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"pikmelo-server/internal/online"
)

// Opt-in because standard Go-only development machines need not install Node.
// All resolver, script, media and lyric traffic stays inside this fixture.
func TestOnlineLiveWorker(t *testing.T) {
	if os.Getenv("MUSIC_SERVER_LIVE_ONLINE_WORKER") != "1" {
		t.Skip("set MUSIC_SERVER_LIVE_ONLINE_WORKER=1 to run the actual Node worker integration")
	}
	node := os.Getenv("MUSIC_SERVER_NODE")
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Fatal("live worker verification requires Node", err)
		}
	}
	modulePath, err := filepath.Abs(filepath.Join("..", "..", "online-worker", "server.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	moduleURL := filepath.ToSlash(modulePath)
	if filepath.VolumeName(modulePath) != "" {
		moduleURL = "/" + moduleURL
	}
	encodedURL, _ := json.Marshal((&url.URL{Scheme: "file", Path: moduleURL}).String())
	// Use dynamic import in an async eval so the file-based worker threads do
	// not inherit --input-type=module, which Node accepts only for eval/stdin.
	program := fmt.Sprintf(`(async()=>{const{createRunnerServer}=await import(%s);const server=createRunnerServer();server.listen(0,'127.0.0.1',()=>console.log(server.address().port));})().catch(error=>{console.error(error);process.exit(1)});`, encodedURL)
	command := exec.Command(node, "--eval", program)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	errorLog, err := os.CreateTemp(t.TempDir(), "online-worker-*.log")
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = errorLog
	if err := command.Start(); err != nil {
		errorLog.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = errorLog.Close()
		if t.Failed() {
			body, _ := os.ReadFile(errorLog.Name())
			t.Logf("Node worker stderr: %s", body)
		}
	})
	portReady := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		if scanner.Scan() {
			portReady <- scanner.Text()
		} else {
			portReady <- ""
		}
	}()
	var port string
	select {
	case port = <-portReady:
	case <-time.After(8 * time.Second):
		t.Fatal("Node worker did not start")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		t.Fatal("Node worker did not report a valid port")
	}

	api, _ := testAPI(t)
	_, _, admin := loginTestAccount(t, api.store, "admin")
	member, token := readyMember(t, api, "online-live-worker")
	audio := validTestAudio(t)
	var resolverCalls atomic.Int32
	const script = `/**
 * @name Integration fixture
 * @version 1.0
 */
const {EVENT_NAMES,on,send,request}=lx;
on(EVENT_NAMES.request,({source,action,info})=>new Promise((resolve,reject)=>{
  if(source!=='kw'||action!=='musicUrl'||info.type!=='128k'||info.musicInfo.songmid!=='remote-one')return reject(new Error('Request metadata mismatch'));
  request('https://resolver.example.test/music',{method:'GET'},(error,response,body)=>{
    if(error)return reject(error);
    const result=typeof body==='string'?JSON.parse(body):body;
    resolve(result.url);
  });
}));
send(EVENT_NAMES.inited,{sources:{kw:{name:'Fixture platform',type:'music',actions:['musicUrl'],qualitys:['128k']}}});`
	client := &http.Client{Transport: onlineFixtureTransport(func(r *http.Request) (*http.Response, error) {
		var body []byte
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		switch r.URL.Hostname() {
		case "source.example.test":
			body = []byte(script)
			headers.Set("Content-Type", "text/javascript")
		case "resolver.example.test":
			resolverCalls.Add(1)
			body = []byte(`{"url":"https://audio.example.test/song.wav"}`)
		case "audio.example.test":
			body = audio
			headers.Set("Content-Type", "audio/wav")
			headers.Set("ETag", `"live-audio"`)
		default:
			body = []byte(`{}`)
		}
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
	})}
	brokerServer := httptest.NewServer(api)
	t.Cleanup(brokerServer.Close)
	api.store.online.client = client
	api.store.online.broker = online.NewBroker(client, brokerServer.URL+"/api/v2/online/broker")
	api.store.online.workerURL = "http://127.0.0.1:" + port
	t.Cleanup(api.store.online.close)
	probeBody, _ := json.Marshal(map[string]any{"script": script, "action": "init"})
	probe, err := http.Post(api.store.online.workerURL+"/execute", "application/json", bytes.NewReader(probeBody))
	if err != nil {
		t.Fatal(err)
	}
	probeResult, _ := io.ReadAll(probe.Body)
	probe.Body.Close()
	if probe.StatusCode != 200 {
		t.Fatalf("actual Node worker initialization: %d %s", probe.StatusCode, probeResult)
	}
	source := onlineFixtureSource(t, api, admin)
	target := Playlist{ID: uuid.NewString(), Name: "Live Node download target", TrackIDs: []string{}}
	if err := api.store.PutPlaylist(target, MutationOptions{UserID: member.ID}); err != nil {
		t.Fatal(err)
	}
	job := onlineFixtureJob(t, api, token, source.ID, target.ID)
	job = onlineAwaitFixtureJob(t, api, job.ID, "completed")
	if resolverCalls.Load() != 1 || job.TrackID == "" || job.Downloaded != int64(len(audio)) {
		t.Fatalf("live worker did not traverse resolver and media pipeline: calls=%d job=%+v", resolverCalls.Load(), job)
	}
	playlist, ok := api.store.Playlist(target.ID)
	if !ok || len(playlist.TrackIDs) != 1 || playlist.TrackIDs[0] != job.TrackID {
		t.Fatal("live worker import was not committed with target playlist")
	}
	track, ok := api.store.Track(job.TrackID)
	if !ok || track.UploadedBy != member.ID || track.Title != "Imported fixture" {
		t.Fatal("live worker import has wrong track metadata or uploader")
	}
}
