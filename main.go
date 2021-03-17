package main

import (
	"encoding/json"
	"flag"
	"fmt"
	_ "github.com/influxdata/influxdb1-client"
	influxdb "github.com/influxdata/influxdb1-client"
	"github.com/nathan-osman/go-sunrise"
	"go.uber.org/zap"
	"net/url"
	"os"
	"time"
)

// Config represents a JSON-formatted config file
type Configuration struct {
	Latitude                float64       `json:"latitude"`
	Longitude               float64       `json:"longitude"`
	PollInterval            time.Duration `json:"poll-interval"`
	InfluxDBURL             string        `json:"influxdb-url"`
	InfluxDBPort            int           `json:"influxdb-port"`
	InfluxDBMeasurement     string        `json:"influxdb-measurement"`
	InfluxDBDatabase        string        `json:"influxdb-database"`
	InfluxDBRetentionPolicy string        `json:"influxdb-retention-policy"`
	InfluxDBSkipVerifySsl   bool          `json:"influxdb-skip-verify-ssl"`
}

// Load a config file and return the Config struct
func LoadConfiguration(logger *zap.Logger, file string) Configuration {

	var config Configuration
	configFile, err := os.Open(file)
	defer configFile.Close()
	if err != nil {
		logger.Warn(fmt.Sprintf("failed to open %s", file),
			zap.String("op", "LoadConfiguration"),
			zap.Error(err),
		)
		return config
	}

	jsonParser := json.NewDecoder(configFile)
	jsonParser.Decode(&config)

	return config
}

func main() {

	// Initialize the structured logger
	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Println("{\"op\": \"main\", \"level\": \"fatal\", \"msg\": \"failed to initiate logger\"}")
		os.Exit(1)
	}
	defer logger.Sync()

	// Load the config file based on path provided via CLI or the default
	configLocation := flag.String("config", "config.json", "path to configuration file")
	flag.Parse()
	config := LoadConfiguration(logger, *configLocation)

	// Initialize the InfluxDB connection
	influxDBAddr := fmt.Sprintf("%s:%d", config.InfluxDBURL, config.InfluxDBPort)
	host, err := url.Parse(influxDBAddr)
	if err != nil {
		logger.Fatal(fmt.Sprintf("failed to parse InfluxDB URL %s", influxDBAddr),
			zap.String("op", "main"),
			zap.Error(err),
		)
	}
	influxConf := influxdb.Config{
		URL:       *host,
		UnsafeSsl: config.InfluxDBSkipVerifySsl,
	}
	influxConn, err := influxdb.NewClient(influxConf)
	if err != nil {
		logger.Fatal("failed to initialize InfluxDB connection",
			zap.String("op", "main"),
			zap.Error(err),
		)
	}

	now := time.Now()
	sunriseTime, sunsetTime := sunrise.SunriseSunset(
		config.Latitude,
		config.Longitude,
		now.Year(),
		now.Month(),
		now.Day(),
	)

	for {

		pollStartTime := int32(time.Now().Unix())

		now = time.Now()
		sunriseTime, sunsetTime = UpdateSunriseSunset(logger, config, sunriseTime, sunsetTime, now)
		daylight := Daylight(sunriseTime, sunsetTime, now)
		err := WriteToInflux(logger, config, influxConn, daylight, now)
		if err != nil {
			logger.Error("failed to write daylight data to InfluxDB",
				zap.String("op", "main"),
				zap.Error(err),
			)
		}

		timeElapsed := int32(time.Now().Unix()) - pollStartTime
		time.Sleep(config.PollInterval*time.Second - time.Duration(timeElapsed)*time.Second)

	}

}

func UpdateSunriseSunset(logger *zap.Logger, config Configuration, currentSunrise time.Time, currentSunset time.Time, t time.Time) (time.Time, time.Time) {
	sunriseTime := currentSunrise
	sunsetTime := currentSunset
	if currentSunrise.Day() == t.Add(-24*time.Hour).Day() ||
		currentSunset.Day() == t.Add(-24*time.Hour).Day() {

		logger.Info("fetching new sunrise and sunset values",
			zap.String("op", "UpdateSunriseSunset"),
		)

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

func WriteToInflux(logger *zap.Logger, config Configuration, influxConn *influxdb.Client, daylight bool, t time.Time) error {
	data := make([]influxdb.Point, 1)
	data[0] = influxdb.Point{
		Measurement: config.InfluxDBMeasurement,
		Tags:        map[string]string{},
		Fields: map[string]interface{}{
			"daylight": daylight,
		},
		Time:      t,
		Precision: "ns",
	}

	batchData := influxdb.BatchPoints{
		Points:          data,
		Database:        config.InfluxDBDatabase,
		RetentionPolicy: config.InfluxDBRetentionPolicy,
	}

	_, err := influxConn.Write(batchData)
	if err != nil {
		return err
	}
	return nil
}
