package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	influx "github.com/influxdata/influxdb-client-go/v2"
	influxAPI "github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/nathan-osman/go-sunrise"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Config represents a YAML-formatted config file
type Configuration struct {
	Latitude     float64
	Longitude    float64
	PollInterval time.Duration
	InfluxDB     InfluxDB
}

type InfluxDB struct {
	Address           string
	Username          string
	Password          string
	MeasurementPrefix string
	Database          string
	RetentionPolicy   string
	Token             string
	Organization      string
	Bucket            string
	SkipVerifySsl     bool
	FlushInterval     uint
}

// Load a config file and return the Config struct
func LoadConfiguration(configPath string) (*Configuration, error) {
	viper.SetConfigFile(configPath)
	viper.AutomaticEnv()
	viper.SetConfigType("yml")

	err := viper.ReadInConfig()
	if err != nil {
		return nil, fmt.Errorf("error reading config file %s, %s", configPath, err)
	}

	var configuration Configuration
	err = viper.Unmarshal(&configuration)
	if err != nil {
		return nil, fmt.Errorf("unable to decode config into struct, %s", err)
	}

	return &configuration, nil
}

type InfluxWriteConfigError struct{}

func (r *InfluxWriteConfigError) Error() string {
	return "must configure at least one of bucket or database/retention policy"
}

func InfluxConnect(config *Configuration) (influx.Client, influxAPI.WriteAPI, error) {
	var auth string
	if config.InfluxDB.Token != "" {
		auth = config.InfluxDB.Token
	} else if config.InfluxDB.Username != "" && config.InfluxDB.Password != "" {
		auth = fmt.Sprintf("%s:%s", config.InfluxDB.Username, config.InfluxDB.Password)
	} else {
		auth = ""
	}

	var writeDest string
	if config.InfluxDB.Bucket != "" {
		writeDest = config.InfluxDB.Bucket
	} else if config.InfluxDB.Database != "" && config.InfluxDB.RetentionPolicy != "" {
		writeDest = fmt.Sprintf("%s/%s", config.InfluxDB.Database, config.InfluxDB.RetentionPolicy)
	} else {
		return nil, nil, &InfluxWriteConfigError{}
	}

	if config.InfluxDB.FlushInterval == 0 {
		config.InfluxDB.FlushInterval = 30
	}

	options := influx.DefaultOptions().
		SetFlushInterval(1000 * config.InfluxDB.FlushInterval).
		SetTLSConfig(&tls.Config{
			InsecureSkipVerify: config.InfluxDB.SkipVerifySsl,
		})
	client := influx.NewClientWithOptions(config.InfluxDB.Address, auth, options)

	writeAPI := client.WriteAPI(config.InfluxDB.Organization, writeDest)

	return client, writeAPI, nil
}

func main() {

	// Load the config file based on path provided via CLI or the default
	configLocation := flag.String("config", "config.yaml", "path to configuration file")
	flag.Parse()
	config, err := LoadConfiguration(*configLocation)
	if err != nil {
		log.WithFields(log.Fields{
			"op":    "main.LoadConfiguration",
			"error": err,
		}).Fatal("failed to load configuration")
	}

	// Initialize the InfluxDB connection
	influxClient, writeAPI, err := InfluxConnect(config)
	if err != nil {
		log.WithFields(log.Fields{
			"op":    "main",
			"error": err,
		}).Fatal("failed to initialize InfluxDB connection")
	}
	defer influxClient.Close()
	defer writeAPI.Flush()

	errorsCh := writeAPI.Errors()

	// Monitor InfluxDB write errors
	go func() {
		for err := range errorsCh {
			log.WithFields(log.Fields{
				"op":    "main",
				"error": err,
			}).Error("encountered error on writing to InfluxDB")
		}
	}()

	// Look for SIGTERM or SIGINT
	cancelCh := make(chan os.Signal, 1)
	signal.Notify(cancelCh, syscall.SIGTERM, syscall.SIGINT)

	now := time.Now()
	sunriseTime, sunsetTime := sunrise.SunriseSunset(
		config.Latitude,
		config.Longitude,
		now.Year(),
		now.Month(),
		now.Day(),
	)

	go func() {
		for {

			pollStartTime := int32(time.Now().Unix())

			now = time.Now()
			sunriseTime, sunsetTime = UpdateSunriseSunset(*config, sunriseTime, sunsetTime, now)
			daylight := Daylight(sunriseTime, sunsetTime, now)
			WriteToInflux(*config, writeAPI, daylight, now)

			timeElapsed := int32(time.Now().Unix()) - pollStartTime
			time.Sleep(config.PollInterval*time.Second - time.Duration(timeElapsed)*time.Second)

		}
	}()

	sig := <-cancelCh
	log.WithFields(log.Fields{
		"op": "main",
	}).Info(fmt.Sprintf("caught signal %v, flushing data to InfluxDB", sig))
	writeAPI.Flush()

}

func UpdateSunriseSunset(config Configuration, currentSunrise time.Time, currentSunset time.Time, t time.Time) (time.Time, time.Time) {
	sunriseTime := currentSunrise
	sunsetTime := currentSunset
	if currentSunrise.Day() == t.Add(-24*time.Hour).Day() ||
		currentSunset.Day() == t.Add(-24*time.Hour).Day() {

		sunriseTime, sunsetTime = sunrise.SunriseSunset(
			config.Latitude,
			config.Longitude,
			t.Year(),
			t.Month(),
			t.Day(),
		)
	}

	return sunriseTime, sunsetTime

}

func Daylight(sunrise time.Time, sunset time.Time, t time.Time) bool {
	if t.Before(sunrise) || t.After(sunset) {
		return false
	} else {
		return true
	}
}

func WriteToInflux(config Configuration, writeAPI influxAPI.WriteAPI, daylight bool, t time.Time) {
	data := influx.NewPoint(
		"daylight",
		map[string]string{},
		map[string]interface{}{
			"daylight": daylight,
		},
		t,
	)

	writeAPI.WritePoint(data)
}
