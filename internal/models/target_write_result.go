// PROPRIETARY AND CONFIDENTIAL
//
// Unauthorized copying of this file via any medium is strictly prohibited.
//
// Copyright (c) 2020-2021 Snowplow Analytics Ltd. All rights reserved.

package models

import (
	"time"

	"github.com/snowplow-devops/stream-replicator/internal/common"
)

// TargetWriteResult contains the results from a target write operation
type TargetWriteResult struct {
	Sent   int64
	Failed int64

	// Delta between TimePulled and TimeOfWrite tells us how well the
	// application is at processing data internally
	MaxProcLatency time.Duration
	MinProcLatency time.Duration
	AvgProcLatency time.Duration

	// Delta between TimeCreated and TimeOfWrite tells us how far behind
	// the application is on the stream it is consuming from
	MaxMsgLatency time.Duration
	MinMsgLatency time.Duration
	AvgMsgLatency time.Duration
}

// NewWriteResult uses the current time as the WriteTime and then calls NewWriteResultWithTime
func NewWriteResult(sent int64, failed int64, messages []*Message) *TargetWriteResult {
	return NewWriteResultWithTime(sent, failed, time.Now().UTC(), messages)
}

// NewWriteResultWithTime builds a result structure to return from a target write
// attempt which contains the sent and failed message counts as well as several
// derived latency measures.
func NewWriteResultWithTime(sent int64, failed int64, timeOfWrite time.Time, messages []*Message) *TargetWriteResult {
	r := TargetWriteResult{
		Sent:   sent,
		Failed: failed,
	}

	messagesLen := int64(len(messages))

	var sumProcLatency time.Duration
	var sumMessageLatency time.Duration

	for _, msg := range messages {
		procLatency := timeOfWrite.Sub(msg.TimePulled)
		if r.MaxProcLatency < procLatency {
			r.MaxProcLatency = procLatency
		}
		if r.MinProcLatency > procLatency || r.MinProcLatency == time.Duration(0) {
			r.MinProcLatency = procLatency
		}
		sumProcLatency += procLatency

		messageLatency := timeOfWrite.Sub(msg.TimeCreated)
		if r.MaxMsgLatency < messageLatency {
			r.MaxMsgLatency = messageLatency
		}
		if r.MinMsgLatency > messageLatency || r.MinMsgLatency == time.Duration(0) {
			r.MinMsgLatency = messageLatency
		}
		sumMessageLatency += messageLatency
	}

	if messagesLen > 0 {
		r.AvgProcLatency = common.GetAverageFromDuration(sumProcLatency, messagesLen)
		r.AvgMsgLatency = common.GetAverageFromDuration(sumMessageLatency, messagesLen)
	}

	return &r
}

// Total returns the sum of Sent + Failed messages
func (wr *TargetWriteResult) Total() int64 {
	return wr.Sent + wr.Failed
}
