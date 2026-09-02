package vtr

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/beevik/ntp"
	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// free no-sign-up weather for everywhere on earth, theoretically
// a user who will never go above, like, 5 requests per second

func GetJdoc() (SettingsJdoc, bool) {
	file, err := os.ReadFile("/data/data/com.anki.victor/persistent/jdocs/vic.RobotSettings.json")
	if err != nil {
		log.Println(err)
	}
	var j SettingsJdoc
	err = json.Unmarshal(file, &j)
	if err != nil {
		fmt.Println("error")
		return j, false
	}
	return j, true
}

var currentTemp string = "120"
var currentCondition WeatherCondition = Snow
var currentLocation string = "San Francisco, California"
var currentUnits string = "F"
var currentWeatherTime time.Time
var weatherMutex sync.Mutex
var resetTicker chan bool

type WeatherCondition string

const (
	Cloudy        WeatherCondition = "Cloudy"
	Windy         WeatherCondition = "Windy"
	Rain          WeatherCondition = "Rain"
	Thunderstorms WeatherCondition = "Thunderstorms"
	Sunny         WeatherCondition = "Sunny"
	Clear         WeatherCondition = "Stars"
	Snow          WeatherCondition = "Snow"
	Cold          WeatherCondition = "Cold"
)

func waitForInternetAndAccurateClock() {
	for {
		if !hasInternet() {
			fmt.Println("no internet")
			time.Sleep(5 * time.Second)
			continue
		}
		ntpTime, err := ntp.Time("time.google.com")
		if err != nil {
			fmt.Println("can't get NTP time :(", err)
			time.Sleep(5 * time.Second)
			continue
		}

		// compare to system time
		systemTime := time.Now().UTC()
		diff := systemTime.Sub(ntpTime)
		if diff < -24*time.Hour || diff > 24*time.Hour {
			fmt.Println("system time is off...")
			time.Sleep(5 * time.Second)
			continue
		}

		fmt.Println("INTERNET UP")
		break
	}
}

func hasInternet() bool {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := client.Get("https://www.google.com/generate_204")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 204
}

func locationFromDisk() (location, units string) {
	botJdoc, jdocExists := GetJdoc()
	fmt.Println(botJdoc)
	if jdocExists {
		currentLocation = botJdoc.Jdoc.DefaultLocation
		if botJdoc.Jdoc.TempIsFahrenheit {
			currentUnits = "F"
		} else {
			currentUnits = "C"
		}
	}
	return currentLocation, currentUnits
}

func FetchWeatherNow(external bool) {
	waitForInternetAndAccurateClock()
	if external {
		// makes it so if resetTicker isn't being received, this goes anyway
		select {
		case resetTicker <- true:
		default:
		}
	}
	weatherMutex.Lock()
	location, units := locationFromDisk()
	log.Println("Location from disk: ", location)
	tempC, tempF, weather, time, err := getWeather(location)
	if err != nil {
		currentTemp = "120"
		currentCondition = "Snow"
		fmt.Println("Error getting weather")
		weatherMutex.Unlock()
		return
	}
	if units == "F" {
		currentTemp = tempF
	} else {
		currentTemp = tempC
	}
	currentCondition = weather
	currentWeatherTime = time
	weatherMutex.Unlock()
}

func WeatherFetcher() {
	FetchWeatherNow(false)
	tickyticktick := time.NewTicker(time.Minute * 30)
	go func() {
		for range resetTicker {
			tickyticktick.Reset(time.Minute * 30)
		}
	}()
	for range tickyticktick.C {
		FetchWeatherNow(false)
	}
}

func getWeather(location string) (tempC, tempF string, weather WeatherCondition, theTime time.Time, err error) {
	geoURL := "https://nominatim.openstreetmap.org/search?" + url.Values{
		"format": {"json"},
		"q":      {location},
	}.Encode()
	req1, _ := http.NewRequest("GET", geoURL, nil)
	req1.Header.Set("User-Agent", "vic-cloudless/1.0")
	res1, err := http.DefaultClient.Do(req1)
	if err != nil {
		return "", "", Cold, time.Time{}, err
	}
	defer res1.Body.Close()

	var geo []struct{ Lat, Lon string }
	if err = json.NewDecoder(res1.Body).Decode(&geo); err != nil {
		return "", "", Cold, time.Time{}, err
	}
	if len(geo) == 0 {
		return "", "", Cold, time.Time{}, fmt.Errorf("no geocode for %q", location)
	}
	lat, lon := geo[0].Lat, geo[0].Lon
	oURL := "https://api.open-meteo.com/v1/forecast?" + url.Values{
		"latitude":        {lat},
		"longitude":       {lon},
		"current_weather": {"true"},
		"daily":           {"sunrise,sunset"},
		"timezone":        {"auto"},
	}.Encode()
	oRes, err := http.Get(oURL)
	if err != nil {
		return "", "", Cold, time.Time{}, err
	}
	defer oRes.Body.Close()

	var om struct {
		UTCOffsetSeconds int `json:"utc_offset_seconds"`
		CurrentWeather   struct {
			Temperature float64 `json:"temperature"`
			WeatherCode int     `json:"weathercode"`
			Time        string  `json:"time"`
		} `json:"current_weather"`
		Daily struct {
			Sunrise []string `json:"sunrise"`
			Sunset  []string `json:"sunset"`
		} `json:"daily"`
	}
	if err = json.NewDecoder(oRes.Body).Decode(&om); err != nil {
		return "", "", Cold, time.Time{}, err
	}

	c := om.CurrentWeather.Temperature
	f := c*9.0/5.0 + 32.0
	tempC = fmt.Sprint(math.Round(c))
	tempF = fmt.Sprint(math.Round(f))

	loc := time.FixedZone("local", om.UTCOffsetSeconds)
	layout := "2006-01-02T15:04"
	ct, err := time.ParseInLocation(layout, om.CurrentWeather.Time, loc)
	if err != nil {
		return tempC, tempF, Cold, time.Time{}, fmt.Errorf("time parse current: %w", err)
	}

	type sunpair struct {
		sunrise time.Time
		sunset  time.Time
	}
	var sunpairs []sunpair
	for i := 0; i < len(om.Daily.Sunrise) && i < len(om.Daily.Sunset); i++ {
		sr, err1 := time.ParseInLocation(layout, om.Daily.Sunrise[i], loc)
		ss, err2 := time.ParseInLocation(layout, om.Daily.Sunset[i], loc)
		if err1 == nil && err2 == nil {
			sunpairs = append(sunpairs, sunpair{sr, ss})
		}
	}

	isDay := false
	for i, sp := range sunpairs {
		if ct.After(sp.sunrise) && ct.Before(sp.sunset) {
			isDay = true
			break
		}
		if i < len(sunpairs)-1 {
			nextSunrise := sunpairs[i+1].sunrise
			if ct.After(sp.sunset) && ct.Before(nextSunrise) {
				isDay = false
				break
			}
		}
	}
	if !isDay && ct.Before(sunpairs[0].sunrise) {
		isDay = false
	}
	code := om.CurrentWeather.WeatherCode
	switch {
	case code >= 95 && code <= 99:
		weather = Thunderstorms
	case code == 71 || code == 73 || code == 75 || code == 77 || code == 85 || code == 86:
		weather = Snow
	case code == 1 || code == 2 || code == 0:
		if isDay {
			weather = Sunny
		} else {
			weather = Clear
		}
	case code == 3 || code == 45 || code == 48:
		weather = Cloudy
	case (code >= 51 && code <= 67) || (code >= 80 && code <= 82):
		weather = Rain
	default:
		weather = Cloudy
	}
	if c <= 0 {
		weather = Cold
	}
	fmt.Println("weather:", weather)

	return tempC, tempF, weather, ct, nil
}
