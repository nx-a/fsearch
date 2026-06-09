package main

import (
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type AuthConfig struct {
	AuthMode               string
	MdpAuthUrl             string
	KeycloakHost           string
	KeycloakRealm          string
	KeycloakOpenKeyFromEnv string
}
type Config struct {
	Db                   string
	ServiceContext       string
	ServicePort          string
	EnableHTTPRequestLog bool
	EnableJWTSecurity    bool
	Port                 string
	SysName              string
	ChannelPrefix        string
	// Role — роль пода: "writer" (писатель, по умолчанию) или "reader" (читатель).
	Role string
	// WriterURL — базовый URL пода-писателя, откуда читатель тянет снапшот.
	WriterURL string
	// SyncIntervalSeconds — период опроса снапшота читателем.
	SyncIntervalSeconds int
	AuthConfig
}

func (c *Config) GetServiceContext() string {
	return c.ServiceContext
}

// IsReadOnly сообщает, что под запущен как читатель (read-only).
func (c *Config) IsReadOnly() bool {
	return strings.EqualFold(c.Role, "reader")
}

// GetWriterURL возвращает URL пода-писателя без хвостового слеша.
func (c *Config) GetWriterURL() string {
	return strings.TrimRight(c.WriterURL, "/")
}

// GetSyncInterval возвращает период синхронизации снапшота.
func (c *Config) GetSyncInterval() time.Duration {
	if c.SyncIntervalSeconds <= 0 {
		return 15 * time.Second
	}
	return time.Duration(c.SyncIntervalSeconds) * time.Second
}

func NewConfig() *Config {
	setDefaults()
	viper.AutomaticEnv()
	return &Config{
		Db:                   viper.GetString("STORAGE_PATH"),
		SysName:              viper.GetString("SERVICE_NAME"),
		ChannelPrefix:        viper.GetString("CHANNEL_PREFIX"),
		ServiceContext:       viper.GetString("SERVICE_NAME"),
		ServicePort:          viper.GetString("SERVICE_PORT"),
		Role:                 viper.GetString("ROLE"),
		WriterURL:            viper.GetString("WRITER_URL"),
		SyncIntervalSeconds:  viper.GetInt("SYNC_INTERVAL_SECONDS"),
		EnableHTTPRequestLog: viper.GetBool("ENABLE_HTTP_REQUEST_LOG"),
		EnableJWTSecurity:    viper.GetBool("SECURITY_ENABLED"),
		AuthConfig: AuthConfig{
			AuthMode:               viper.GetString("AUTH_SERVICE"),
			MdpAuthUrl:             viper.GetString("AUTH_SERVER_URL"),
			KeycloakHost:           viper.GetString("KEYCLOAK_HOST"),
			KeycloakRealm:          getStringOrDefault("KEYCLOAK_REALM", getStringOrDefault("REALM", "diasoft")),
			KeycloakOpenKeyFromEnv: viper.GetString("AUTH_SERVER_PUBLIC_KEY"),
		}}
}
func getStringOrDefault(key string, def string) string {
	v := os.Getenv(key)
	if len(v) == 0 {
		return def
	}
	return v
}
func setDefaults() {
	viper.SetDefault("STORAGE_PATH", "fsearch.db")
	viper.SetDefault("SERVICE_NAME", "fsearch")
	viper.SetDefault("CHANNEL_PREFIX", "")
	viper.SetDefault("AUTH_SERVICE", "mdpauth")
	viper.SetDefault("AUTH_SERVER_URL", "http://mdpauth/mdpauth/oauth/token_key")
	viper.SetDefault("KEYCLOAK_HOST", "https://login.diasoft.ru")
	viper.SetDefault("KEYCLOAK_REALM", "qwork")
	viper.SetDefault("AUTH_SERVER_PUBLIC_KEY", "nil")
	viper.SetDefault("SERVICE_PORT", "8080")
	viper.SetDefault("ROLE", "writer")
	viper.SetDefault("WRITER_URL", "")
	viper.SetDefault("SYNC_INTERVAL_SECONDS", 15)
	viper.SetDefault("READ_BUFFER_SIZE", 1*1024*1024)
	viper.SetDefault("MAX_REQUEST_BODY_SIZE", 100*1024*1024)
	viper.SetDefault("ENABLE_HTTP_REQUEST_LOG", true)
	viper.SetDefault("SECURITY_ENABLED", false)

}
