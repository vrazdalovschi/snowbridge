// PROPRIETARY AND CONFIDENTIAL
//
// Unauthorized copying of this file via any medium is strictly prohibited.
//
// Copyright (c) 2020-2021 Snowplow Analytics Ltd. All rights reserved.

package source

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/aws/aws-sdk-go/service/sqs/sqsiface"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/twinj/uuid"
	"strconv"
	"sync"
	"time"

	"github.com/snowplow-devops/stream-replicator/internal/common"
	"github.com/snowplow-devops/stream-replicator/internal/models"
	"github.com/snowplow-devops/stream-replicator/internal/source/sourceiface"
)

// SQSSource holds a new client for reading messages from SQS
type SQSSource struct {
	client           sqsiface.SQSAPI
	queueName        string
	concurrentWrites int

	log *log.Entry

	// exitSignal holds a channel for signalling an end to the read loop
	exitSignal chan struct{}

	// processErrorSignal holds a channel for handling processing errors
	// and exiting the read loop on the first error discovered
	processErrorSignal chan error
}

// NewSQSSource creates a new client for reading messages from SQS
func NewSQSSource(concurrentWrites int, region string, queueName string, roleARN string) (*SQSSource, error) {
	awsSession, awsConfig := common.GetAWSSession(region, roleARN)
	sqsClient := sqs.New(awsSession, awsConfig)

	return NewSQSSourceWithInterfaces(sqsClient, concurrentWrites, region, queueName)
}

// NewSQSSourceWithInterfaces allows you to provide an SQS client directly to allow
// for mocking and localstack usage
func NewSQSSourceWithInterfaces(client sqsiface.SQSAPI, concurrentWrites int, region string, queueName string) (*SQSSource, error) {
	return &SQSSource{
		client:             client,
		queueName:          queueName,
		concurrentWrites:   concurrentWrites,
		log:                log.WithFields(log.Fields{"source": "sqs", "cloud": "AWS", "region": region, "queue": queueName}),
		exitSignal:         make(chan struct{}),
		processErrorSignal: make(chan error, concurrentWrites),
	}, nil
}

// Read will pull messages from the noted SQS queue forever
func (ss *SQSSource) Read(sf *sourceiface.SourceFunctions) error {
	ss.log.Info("Reading messages from queue ...")

	urlResult, err := ss.client.GetQueueUrl(&sqs.GetQueueUrlInput{
		QueueName: aws.String(ss.queueName),
	})
	if err != nil {
		return errors.Wrap(err, "Failed to get SQS queue URL")
	}
	queueURL := urlResult.QueueUrl

	throttle := make(chan struct{}, ss.concurrentWrites)
	wg := sync.WaitGroup{}

	var processErr error

ProcessLoop:
	for {
		select {
		case <-ss.exitSignal:
			break ProcessLoop
		case processErr = <-ss.processErrorSignal:
			break ProcessLoop
		default:
			throttle <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := ss.process(queueURL, sf)
				if err != nil {
					ss.processErrorSignal <- err
				}
				<-throttle
			}()
		}
	}
	wg.Wait()

	return processErr
}

func (ss *SQSSource) process(queueURL *string, sf *sourceiface.SourceFunctions) error {
	msgRes, err := ss.client.ReceiveMessage(&sqs.ReceiveMessageInput{
		AttributeNames: []*string{
			aws.String(sqs.MessageSystemAttributeNameSentTimestamp),
		},
		MessageAttributeNames: []*string{
			aws.String(sqs.QueueAttributeNameAll),
		},
		QueueUrl:            queueURL,
		MaxNumberOfMessages: aws.Int64(10),
		VisibilityTimeout:   aws.Int64(10),
		WaitTimeSeconds:     aws.Int64(1),
	})
	if err != nil {
		return errors.Wrap(err, "Failed to get message from SQS queue")
	}
	timePulled := time.Now().UTC()

	var messages []*models.Message
	for _, msg := range msgRes.Messages {
		receiptHandle := msg.ReceiptHandle

		ackFunc := func() {
			ss.log.Debugf("Deleting message with receipt handle: %s", *receiptHandle)
			_, err := ss.client.DeleteMessage(&sqs.DeleteMessageInput{
				QueueUrl:      queueURL,
				ReceiptHandle: receiptHandle,
			})
			if err != nil {
				err = errors.Wrap(err, "Failed to delete message from SQS queue")
				ss.log.WithFields(log.Fields{"error": err}).Error(err)
			}
		}

		var timeCreated time.Time
		timeCreatedStr, ok := msg.Attributes[sqs.MessageSystemAttributeNameSentTimestamp]
		if ok {
			timeCreatedMillis, err := strconv.ParseInt(*timeCreatedStr, 10, 64)
			if err != nil {
				err = errors.Wrap(err, "Failed to parse SentTimestamp from SQS message")
				ss.log.WithFields(log.Fields{"error": err}).Error(err)

				timeCreated = timePulled
			} else {
				timeCreated = time.Unix(0, timeCreatedMillis*int64(time.Millisecond)).UTC()
			}
		} else {
			ss.log.Warn("Failed to extract SentTimestamp from SQS message attributes")
			timeCreated = timePulled
		}

		messages = append(messages, &models.Message{
			Data:         []byte(*msg.Body),
			PartitionKey: uuid.NewV4().String(),
			AckFunc:      ackFunc,
			TimeCreated:  timeCreated,
			TimePulled:   timePulled,
		})
	}

	err = sf.WriteToTarget(messages)
	if err != nil {
		ss.log.WithFields(log.Fields{"error": err}).Error(err)
	}
	return nil
}

// Stop will halt the reader processing more events
func (ss *SQSSource) Stop() {
	ss.log.Warn("Cancelling SQS receive ...")
	ss.exitSignal <- struct{}{}
}
