package publisher

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/raintank/raintank-metric/msg"
	"github.com/raintank/raintank-metric/schema"
	"github.com/raintank/worldping-api/pkg/log"
)

var (
	Publisher            *Tsdb
	maxMetricsPerPayload = 3000
)

func Init(u *url.URL, apiKey string) {
	Publisher = NewTsdb(u, apiKey)
}

type Tsdb struct {
	sync.Mutex
	Url          *url.URL
	ApiKey       string
	Metrics      []*schema.MetricData
	Events       chan *schema.ProbeEvent
	triggerFlush chan struct{}
	LastFlush    time.Time
	closeChan    chan struct{}
}

func NewTsdb(u *url.URL, apiKey string) *Tsdb {
	t := &Tsdb{
		Metrics:      make([]*schema.MetricData, 0),
		triggerFlush: make(chan struct{}),
		Events:       make(chan *schema.ProbeEvent, 1000),
		Url:          u,
		ApiKey:       apiKey,
	}
	go t.Run()
	return t
}

func (t *Tsdb) Add(metrics []*schema.MetricData) {
	t.Lock()
	t.Metrics = append(t.Metrics, metrics...)
	if len(t.Metrics) > maxMetricsPerPayload {
		t.triggerFlush <- struct{}{}
	}
	t.Unlock()
}
func (t *Tsdb) AddEvent(event *schema.ProbeEvent) {
	t.Events <- event
}

func (t *Tsdb) Flush() {
	t.Lock()
	if len(t.Metrics) == 0 {
		t.Unlock()
		return
	}
	t.LastFlush = time.Now()
	metrics := make([]*schema.MetricData, len(t.Metrics))
	copy(metrics, t.Metrics)
	t.Metrics = t.Metrics[:0]
	t.Unlock()
	// Write the metrics to our HTTP server.
	log.Debug("writing metrics to API", "count", len(metrics))
	id := t.LastFlush.UnixNano()
	body, err := msg.CreateMsg(metrics, id, msg.FormatMetricDataArrayMsgp)
	if err != nil {
		log.Error(3, "unable to convert metrics to MetricDataArrayMsgp.", "error", err)
		return
	}
	sent := false
	for !sent {
		if err = t.PostData("metrics", body); err != nil {
			log.Error(3, err.Error())
			time.Sleep(time.Second)
		} else {
			sent = true
		}
	}
}

func (t *Tsdb) Run() {
	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-ticker.C:
			t.Flush()
		case <-t.triggerFlush:
			t.Flush()
		case e := <-t.Events:
			t.SendEvent(e)
		case <-t.closeChan:
			return
		}
	}
}
func (t *Tsdb) Close() {
	t.triggerFlush <- struct{}{}
	t.closeChan <- struct{}{}
}

func (t *Tsdb) PostData(path string, body []byte) error {
	u := t.Url.String() + path
	req, err := http.NewRequest("POST", u, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "rt-metric-binary")
	req.Header.Set("Authorization", "Bearer "+t.ApiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	respBody, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Posting data failed. %d - %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (t *Tsdb) SendEvent(event *schema.ProbeEvent) {
	id := time.Now().UnixNano()
	body, err := msg.CreateProbeEventMsg(event, id, msg.FormatProbeEventMsgp)
	if err != nil {
		log.Error(3, "Unable to convert event to ProbeEventMsgp.", "error", err)
		return
	}
	sent := false
	for !sent {
		if err = t.PostData("events", body); err != nil {
			log.Error(3, err.Error())
			time.Sleep(time.Second)
		} else {
			sent = true
		}
	}
}