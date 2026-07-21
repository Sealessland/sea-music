package config

import (
	"os"
	"time"
)

const minimumTokenKeyBytes = 32

const localDatabaseURL = "postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music?sslmode=disable"

// LookupEnv abstracts environment lookup so configuration parsing stays deterministic in tests.
type LookupEnv func(string) (string, bool)

// Config is shared by the API and worker process composition roots.
type Config struct {
	Environment string
	LogLevel    string
	Auth        Auth
	Database    Database
	Redis       Redis
	ObjectStore ObjectStore
	Worker      Worker
	Broker      Broker
	Events      Events
	Social      Social
	Telemetry   Telemetry
	RateLimit   RateLimit
	HTTP        HTTP
	Moderation  Moderation
}

type Auth struct {
	TokenKey   string
	Issuer     string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

type Database struct {
	URL              string
	MaxOpen          int
	MaxIdle          int
	ConnectionMaxAge time.Duration
}

type Redis struct {
	URL string
}

type ObjectStore struct {
	Endpoint             string
	Region               string
	Bucket               string
	AccessKey            string
	SecretKey            string
	UploadTTL            time.Duration
	PlaybackURLTTL       time.Duration
	MaxUploadBytes       int64
	DisableDownloadCache bool
}

type Worker struct {
	PollInterval              time.Duration
	LeaseDuration             time.Duration
	MediaTimeout              time.Duration
	QueuedActivationInterval  time.Duration
	QueuedActivationThreshold time.Duration
	FFprobePath               string
	FFmpegPath                string
}

type Broker struct {
	Driver       string
	Brokers      []string
	AccessKey    string
	AccessSecret string
}

type Events struct {
	PollInterval  time.Duration
	LeaseDuration time.Duration
	BatchSize     int
}

type Social struct {
	ReconcileInterval time.Duration
	ReconcileBatch    int
}

type Telemetry struct {
	OTLPEndpoint string
}

type RateLimit struct {
	IdentityWriteRate  float64
	IdentityWriteBurst int
	IdentityReadRate   float64
	IdentityReadBurst  int
}

type HTTP struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ReadinessTimeout  time.Duration
	ShutdownTimeout   time.Duration
	AllowedOrigins    []string
}

type Moderation struct {
	GRPCAddress       string
	MetricsAddress    string
	AgentAddress      string
	PolicyVersion     string
	Mode              string
	Provider          string
	ProviderAPIKey    string
	ProviderBaseURL   string
	ProviderModel     string
	Insecure          bool
	TLSCertFile       string
	TLSKeyFile        string
	TLSCAFile         string
	TLSServerName     string
	PollInterval      time.Duration
	LeaseDuration     time.Duration
	EvaluationTimeout time.Duration
	RPCTimeout        time.Duration
	ApproveThreshold  float64
	RejectThreshold   float64
}

// Load reads process configuration from the environment.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}
