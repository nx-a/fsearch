package event

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"time"

	"gitflex.diasoft.ru/mvp-go/golang-libraries/go-kafka.git/config"
	"gitflex.diasoft.ru/mvp-go/golang-libraries/go-kafka.git/kafka"
	segmentio "github.com/segmentio/kafka-go"
	"software.sslmate.com/src/go-pkcs12"
)

// defaultWriterPoolSize — размер пула segmentio.Writer.
// Переопределяется переменной окружения KAFKA_WRITER_POOL_SIZE.
const defaultWriterPoolSize = 32

type Out struct {
	writers    chan *segmentio.Writer
	cert       *x509.Certificate
	privateKey interface{}
}

func NewOut() *Out {
	out := &Out{}
	size := envInt("KAFKA_WRITER_POOL_SIZE", defaultWriterPoolSize)
	out.writers = make(chan *segmentio.Writer, size)
	for i := 0; i < size; i++ {
		out.writers <- out.initKafkaWriter()
	}
	return out
}

// envInt читает целочисленную переменную окружения, возвращая def при
// отсутствии или некорректном (≤ 0) значении. Используется во всём пакете event.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func (out *Out) WriteMessage(ctx context.Context, topic string, message []byte, headers []kafka.Header) error {
	sm := convertMessageToSegmentio(buildMessage(topic, message, headers...))
	if err := out.produce(ctx, sm); err != nil {
		log.Printf("write message failed: %e", err)
		return err
	}
	return nil
}

func (out *Out) produce(ctx context.Context, m *segmentio.Message) error {
	// Берём writer из пула и обязательно возвращаем его обратно.
	w := <-out.writers
	defer func() { out.writers <- w }()

	const retries = 3
	var errTmp error
	for i := 0; i < retries; i++ {
		reconnect, err := out.retry(ctx, w, m)
		if err == nil {
			return nil
		}
		log.Printf("Error: %e retry step %d", err, i)
		errTmp = err
		if reconnect {
			_ = w.Close()
			w = out.initKafkaWriter()
		}
	}
	return fmt.Errorf("produce message to topic: %s, "+
		"address: %s, tls enabled: %v, batch bytes: %v failed: %w",
		m.Topic, w.Addr, config.LoadConfig().EnableTLS, w.BatchBytes, errTmp)
}

// retry выполняет одну попытку записи. Второе возвращаемое значение сообщает
// produce о необходимости пересоздать writer перед следующей попыткой.
func (out *Out) retry(ctx context.Context, w *segmentio.Writer, m *segmentio.Message) (bool, error) {
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.WriteMessages(ctx, *m); err != nil {
		log.Println("WriteMessages error:" + err.Error() + "\n " + fmt.Sprintf("Тип ошибки: %T\n", err) + fmt.Sprintf("Подробно: %+v\n", err))
		reconnect := false
		if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, segmentio.UnknownTopicOrPartition) {
			log.Println("ErrClosedPipe || UnknownTopicOrPartition. Reconnecting...")
			reconnect = true
		}
		if errors.Is(err, segmentio.LeaderNotAvailable) || errors.Is(err, context.DeadlineExceeded) {
			log.Println("LeaderNotAvailable || DeadlineExceeded. time.Sleep...")
			time.Sleep(time.Millisecond * 250)
		}
		return reconnect, err
	}
	return false, nil
}
func buildMessage(topic string, v []byte, headers ...kafka.Header) *kafka.Message {
	headerList := make([]kafka.Header, 0, len(headers))
	headerList = append(headerList, headers...)
	return &kafka.Message{Topic: topic, Value: v, Headers: headerList}
}
func convertMessageToSegmentio(m *kafka.Message) *segmentio.Message {
	headers := make([]segmentio.Header, 0, len(m.Headers))
	for _, h := range m.Headers {
		headers = append(headers, segmentio.Header{Key: h.Key, Value: h.Value})
	}
	return &segmentio.Message{
		Topic:   m.Topic,
		Value:   m.Value,
		Headers: headers,
	}
}

// tlsKafkaConfig - включает в себя настройки системных переменных.
type tlsKafkaConfig struct {
	isEnabledTls       bool
	serverName         string
	keyStorePath       string
	trustStorePath     string
	keyStorePassword   string
	trustStorePassword string
}

func (c *tlsKafkaConfig) withEnvs() {
	c.isEnabledTls = config.LoadConfig().EnableTLS
	c.serverName = config.LoadConfig().KafkaHost
	c.keyStorePath = config.LoadConfig().KeystoreLocation
	c.trustStorePath = config.LoadConfig().TruststoreLocation
	c.keyStorePassword = config.LoadConfig().KeystorePassword
	c.trustStorePassword = config.LoadConfig().TruststorePassword
}
func (out *Out) initKafkaWriter() *segmentio.Writer {
	brokers := config.LoadConfig().Brokers()
	tlsConf := tlsKafkaConfig{}
	tlsConf.withEnvs()
	dialer := out.getSecuredDialerWithConfig(tlsConf)

	w := &segmentio.Writer{
		BatchBytes:   int64(config.LoadConfig().KafkaProducerMaxReqSize),
		BatchTimeout: time.Duration(config.LoadConfig().KafkaProducerBatchTimeoutMs) * time.Millisecond,
	}
	w.Addr = segmentio.TCP(brokers...)
	w.Balancer = &segmentio.RoundRobin{}
	w.AllowAutoTopicCreation = config.LoadConfig().EnableAutoCreateTopic

	if dialer != nil {
		w.Transport = &segmentio.Transport{
			TLS: dialer.TLS,
		}
	}
	return w
}
func (out *Out) getSecuredDialerWithConfig(config tlsKafkaConfig) *segmentio.Dialer {
	if !config.isEnabledTls {
		return nil
	}

	tlsConfig := &tls.Config{
		RootCAs:              x509.NewCertPool(),
		ServerName:           config.serverName,
		GetClientCertificate: out.getClientCertificate,
		InsecureSkipVerify:   true,
	}

	trustStoreCerts := config.getTruststoreCerts()

	for _, trustStoreCert := range trustStoreCerts {
		tlsConfig.RootCAs.AddCert(trustStoreCert)
	}

	out.privateKey, out.cert = config.getKeystoreCerts()

	return &segmentio.Dialer{
		Timeout: 10 * time.Second,
		TLS:     tlsConfig,
	}
}

// getClientCertificate возвращает клиентский сертификат.
func (out *Out) getClientCertificate(c *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return &tls.Certificate{
		Certificate: [][]byte{out.cert.Raw},
		PrivateKey:  out.privateKey,
		Leaf:        out.cert,
	}, nil
}
func (c *tlsKafkaConfig) getKeystoreCerts() (interface{}, *x509.Certificate) {
	keyStoreBytes, err := os.ReadFile(c.keyStorePath)
	if err != nil {
		panic(err)
	}

	privateKey, cert, _, err := pkcs12.DecodeChain(keyStoreBytes, c.keyStorePassword)
	if err != nil {
		panic(err)
	}

	return privateKey, cert
}

// getTruststoreCerts - определяет источиники, которым доверяет клиент.
func (c *tlsKafkaConfig) getTruststoreCerts() []*x509.Certificate {
	trustStoreBytes, err := os.ReadFile(c.trustStorePath)
	if err != nil {
		panic(err)
	}

	trustStoreCerts, err := pkcs12.DecodeTrustStore(trustStoreBytes, c.trustStorePassword)
	if err != nil {
		panic(err)
	}
	return trustStoreCerts
}
