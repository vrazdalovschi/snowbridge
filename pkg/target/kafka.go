// PROPRIETARY AND CONFIDENTIAL
//
// Unauthorized copying of this file via any medium is strictly prohibited.
//
// Copyright (c) 2020-2021 Snowplow Analytics Ltd. All rights reserved.

package target

import (
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"hash"
	"io/ioutil"
	"strings"
	"time"

	"github.com/Shopify/sarama"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/snowplow-devops/stream-replicator/pkg/models"
	"github.com/xdg/scram"
)

type KafkaConfig struct {
	Brokers        string
	TopicName      string
	TargetVersion  string
	MaxRetries     int
	ByteLimit      int
	Compress       bool
	WaitForAll     bool
	Idempotent     bool
	EnableSASL     bool
	SASLUsername   string
	SASLPassword   string
	SASLAlgorithm  string
	CertFile       string
	KeyFile        string
	CaFile         string
	SkipVerifyTls  bool
	ForceSync      bool
	FlushFrequency int
	FlushMessages  int
	FlushBytes     int
}

// KafkaTarget holds a new client for writing messages to Apache Kafka
type KafkaTarget struct {
	syncProducer     sarama.SyncProducer
	asyncProducer    sarama.AsyncProducer
	asyncResults     chan *SaramaResult
	topicName        string
	brokers          string
	messageByteLimit int

	log *log.Entry
}

type SaramaResult struct {
	Msg *sarama.ProducerMessage
	Err error
}

// NewKafkaTarget creates a new client for writing messages to Apache Kafka
func NewKafkaTarget(cfg *KafkaConfig) (*KafkaTarget, error) {
	kafkaVersion, err := getKafkaVersion(cfg.TargetVersion)
	if err != nil {
		return nil, err
	}

	logger := log.WithFields(log.Fields{"target": "kafka", "brokers": cfg.Brokers, "topic": cfg.TopicName, "version": kafkaVersion})
	sarama.Logger = logger

	saramaConfig := sarama.NewConfig()
	saramaConfig.ClientID = "snowplow_stream_replicator"
	saramaConfig.Version = kafkaVersion
	saramaConfig.Producer.Retry.Max = cfg.MaxRetries
	saramaConfig.Producer.MaxMessageBytes = cfg.ByteLimit

	// Must be enabled for the SyncProducer
	saramaConfig.Producer.Return.Successes = true
	saramaConfig.Producer.Return.Errors = true

	if cfg.WaitForAll {
		saramaConfig.Producer.RequiredAcks = sarama.WaitForAll // Wait for all in-sync replicas to ack the message
	}

	if cfg.Idempotent {
		saramaConfig.Producer.RequiredAcks = sarama.WaitForAll // Wait for all in-sync replicas to ack the message
		saramaConfig.Producer.Idempotent = true
		saramaConfig.Net.MaxOpenRequests = 1
	}

	if cfg.Compress {
		saramaConfig.Producer.Compression = sarama.CompressionSnappy // Compress messages
	}

	if cfg.EnableSASL {
		saramaConfig.Net.SASL.Enable = true
		saramaConfig.Net.SASL.User = cfg.SASLUsername
		saramaConfig.Net.SASL.Password = cfg.SASLPassword
		saramaConfig.Net.SASL.Handshake = true
		if cfg.SASLAlgorithm == "sha512" {
			saramaConfig.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA512} }
			saramaConfig.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA512
		} else if cfg.SASLAlgorithm == "sha256" {
			saramaConfig.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA256} }
			saramaConfig.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA256
		} else if cfg.SASLAlgorithm == "plaintext" {
			saramaConfig.Net.SASL.Mechanism = sarama.SASLTypePlaintext
		} else {
			return nil, fmt.Errorf("invalid SHA algorithm \"%s\": can be either \"sha256\" or \"sha512\"", cfg.SASLAlgorithm)
		}
	}

	tlsConfig, err := createTlsConfiguration(cfg.CertFile, cfg.KeyFile, cfg.CaFile, cfg.SkipVerifyTls)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		saramaConfig.Net.TLS.Config = tlsConfig
		saramaConfig.Net.TLS.Enable = true
	}

	var asyncResults chan *SaramaResult = nil
	var asyncProducer sarama.AsyncProducer = nil
	var syncProducer sarama.SyncProducer = nil
	var producerError error = nil

	// If we don't change the flush settings, sarama will try to produce messages
	// as fast as possible to keep latency low.
	if !cfg.ForceSync {
		saramaConfig.Producer.Flush.Messages = cfg.FlushMessages
		saramaConfig.Producer.Flush.Bytes = cfg.FlushBytes
		saramaConfig.Producer.Flush.Frequency = time.Duration(cfg.FlushFrequency) * time.Millisecond
	}

	// On the broker side, you may want to change the following settings to get stronger consistency guarantees:
	// - For your broker, set `unclean.leader.election.enable` to false
	// - For the topic, you could increase `min.insync.replicas`.
	if !cfg.ForceSync {
		asyncProducer, producerError = sarama.NewAsyncProducer(strings.Split(cfg.Brokers, ","), saramaConfig)

		asyncResults = make(chan *SaramaResult)

		go func() {
			for err := range asyncProducer.Errors() {
				asyncResults <- &SaramaResult{Msg: err.Msg, Err: err.Err}
			}
		}()

		go func() {
			for success := range asyncProducer.Successes() {
				asyncResults <- &SaramaResult{Msg: success}
			}
		}()
	} else {
		syncProducer, producerError = sarama.NewSyncProducer(strings.Split(cfg.Brokers, ","), saramaConfig)
	}

	return &KafkaTarget{
		syncProducer:     syncProducer,
		asyncProducer:    asyncProducer,
		asyncResults:     asyncResults,
		brokers:          cfg.Brokers,
		topicName:        cfg.TopicName,
		messageByteLimit: cfg.ByteLimit,
		log:              logger,
	}, producerError
}

// Write pushes all messages to the required target
func (kt *KafkaTarget) Write(messages []*models.Message) (*models.TargetWriteResult, error) {
	kt.log.Debugf("Writing %d messages to topic ...", len(messages))

	safeMessages, oversized := models.FilterOversizedMessages(
		messages,
		kt.MaximumAllowedMessageSizeBytes(),
	)

	var sent []*models.Message
	var failed []*models.Message
	var errResult error

	if kt.asyncProducer != nil {
		for _, msg := range safeMessages {
			kt.asyncProducer.Input() <- &sarama.ProducerMessage{
				Topic:    kt.topicName,
				Key:      sarama.StringEncoder(msg.PartitionKey),
				Value:    sarama.ByteEncoder(msg.Data),
				Metadata: msg,
			}
		}

		for i := 0; i < len(safeMessages); i++ {

			result := <-kt.asyncResults // Block until result is returned

			if result.Err != nil {
				errResult = multierror.Append(errResult, result.Err)
				originalMessage := result.Msg.Metadata.(*models.Message)
				originalMessage.SetError(result.Err)
				failed = append(failed, originalMessage)
			} else {
				originalMessage := result.Msg.Metadata.(*models.Message)
				if originalMessage.AckFunc != nil {
					originalMessage.AckFunc()
				}
				sent = append(sent, originalMessage)
			}
		}
	} else if kt.syncProducer != nil {
		for _, msg := range safeMessages {
			_, _, err := kt.syncProducer.SendMessage(&sarama.ProducerMessage{
				Topic: kt.topicName,
				Key:   sarama.StringEncoder(msg.PartitionKey),
				Value: sarama.ByteEncoder(msg.Data),
			})

			if err != nil {
				errResult = multierror.Append(errResult, err)
				msg.SetError(err)
				failed = append(failed, msg)
			} else {
				if msg.AckFunc != nil {
					msg.AckFunc()
				}
				sent = append(sent, msg)
			}
		}
	} else {
		errResult = multierror.Append(errResult, fmt.Errorf("no producer has been configured"))
	}

	if errResult != nil {
		errResult = errors.Wrap(errResult, fmt.Sprintf("Error writing messages to Kafka topic: %v", kt.topicName))
	}

	kt.log.Debugf("Successfully wrote %d/%d messages", len(sent), len(safeMessages))
	return models.NewTargetWriteResult(
		sent,
		failed,
		oversized,
		nil,
	), errResult
}

// Open does not do anything for this target
func (kt *KafkaTarget) Open() {}

// Close stops the producer
func (kt *KafkaTarget) Close() {
	kt.log.Warnf("Closing Kafka target for topic '%s'", kt.topicName)

	if kt.asyncProducer != nil {
		if err := kt.asyncProducer.Close(); err != nil {
			kt.log.Fatal("Failed to close producer:", err)
		}
	}

	if kt.syncProducer != nil {
		if err := kt.syncProducer.Close(); err != nil {
			kt.log.Fatal("Failed to close producer:", err)
		}
	}
}

// MaximumAllowedMessageSizeBytes returns the max number of bytes that can be sent
// per message for this target
func (kt *KafkaTarget) MaximumAllowedMessageSizeBytes() int {
	return kt.messageByteLimit
}

// GetID returns the identifier for this target
func (kt *KafkaTarget) GetID() string {
	return fmt.Sprintf("brokers:%s:topic:%s", kt.brokers, kt.topicName)
}

func getKafkaVersion(targetVersion string) (sarama.KafkaVersion, error) {
	preferredVersion := sarama.DefaultVersion

	if targetVersion != "" {
		parsedVersion, err := sarama.ParseKafkaVersion(targetVersion)
		if err != nil {
			return sarama.DefaultVersion, err
		}

		supportedVersion := false
		for _, version := range sarama.SupportedVersions {
			if version == parsedVersion {
				supportedVersion = true
				preferredVersion = parsedVersion
				break
			}
		}
		if !supportedVersion {
			return sarama.DefaultVersion, fmt.Errorf("unsupported version `%s`. select older, compatible version instead", parsedVersion)
		}
	}

	return preferredVersion, nil
}

func createTlsConfiguration(certFile string, keyFile string, caFile string, skipVerify bool) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}

	caCert, err := ioutil.ReadFile(caFile)
	if err != nil {
		return nil, err
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: skipVerify,
	}, nil
}

var SHA256 scram.HashGeneratorFcn = func() hash.Hash { return sha256.New() }
var SHA512 scram.HashGeneratorFcn = func() hash.Hash { return sha512.New() }

type XDGSCRAMClient struct {
	*scram.Client
	*scram.ClientConversation
	scram.HashGeneratorFcn
}

func (x *XDGSCRAMClient) Begin(userName, password, authzID string) (err error) {
	x.Client, err = x.HashGeneratorFcn.NewClient(userName, password, authzID)
	if err != nil {
		return err
	}
	x.ClientConversation = x.Client.NewConversation()
	return nil
}

func (x *XDGSCRAMClient) Step(challenge string) (response string, err error) {
	response, err = x.ClientConversation.Step(challenge)
	return
}

func (x *XDGSCRAMClient) Done() bool {
	return x.ClientConversation.Done()
}