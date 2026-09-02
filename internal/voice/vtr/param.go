package vtr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type SettingsJdoc struct {
	ClientMetadata         string `json:"client_metadata"`
	CloudAccessed          bool   `json:"cloud_accessed"`
	CloudDirtyRemainingSec int    `json:"cloud_dirty_remaining_sec"`
	CloudGetTime           int    `json:"cloud_get_time"`
	DocVersion             int    `json:"doc_version"`
	FmtVersion             int    `json:"fmt_version"`
	Jdoc                   struct {
		ButtonWakeword int  `json:"button_wakeword"`
		Clock24Hour    bool `json:"clock_24_hour"`
		CustomEyeColor struct {
			Enabled    bool `json:"enabled"`
			Hue        int  `json:"hue"`
			Saturation int  `json:"saturation"`
		} `json:"custom_eye_color"`
		DefaultLocation  string `json:"default_location"`
		DistIsMetric     bool   `json:"dist_is_metric"`
		EyeColor         int    `json:"eye_color"`
		Locale           string `json:"locale"`
		MasterVolume     int    `json:"master_volume"`
		TempIsFahrenheit bool   `json:"temp_is_fahrenheit"`
		TimeZone         string `json:"time_zone"`
	} `json:"jdoc"`
}

// FUCK this STUpid fucking function
// do better, kerigan
func ParamChecker(intent string, speechText string, botSerial string) (string, map[string]string, bool) {
	var intentParam string
	var intentParamValue string
	var newIntent string
	var isParam bool
	var intentParams map[string]string
	var botPlaySpecific bool = false
	var botIsEarlyOpus bool = false

	if botPlaySpecific {
		if strings.Contains(intent, "intent_play_blackjack") {
			isParam = true
			newIntent = "intent_play_specific_extend"
			intentParam = "entity_behavior"
			intentParamValue = "blackjack"
			intentParams = map[string]string{intentParam: intentParamValue}
		} else if strings.Contains(intent, "intent_play_fistbump") {
			isParam = true
			newIntent = "intent_play_specific_extend"
			intentParam = "entity_behavior"
			intentParamValue = "fist_bump"
			intentParams = map[string]string{intentParam: intentParamValue}
		} else if strings.Contains(intent, "intent_play_rollcube") {
			isParam = true
			newIntent = "intent_play_specific_extend"
			intentParam = "entity_behavior"
			intentParamValue = "roll_cube"
			intentParams = map[string]string{intentParam: intentParamValue}
		} else if strings.Contains(intent, "intent_play_popawheelie") {
			isParam = true
			newIntent = "intent_play_specific_extend"
			intentParam = "entity_behavior"
			intentParamValue = "pop_a_wheelie"
			intentParams = map[string]string{intentParam: intentParamValue}
		} else if strings.Contains(intent, "intent_play_pickupcube") {
			isParam = true
			newIntent = "intent_play_specific_extend"
			intentParam = "entity_behavior"
			intentParamValue = "pick_up_cube"
			intentParams = map[string]string{intentParam: intentParamValue}
		} else if strings.Contains(intent, "intent_play_keepaway") {
			isParam = true
			newIntent = "intent_play_specific_extend"
			intentParam = "entity_behavior"
			intentParamValue = "keep_away"
			intentParams = map[string]string{intentParam: intentParamValue}
		} else {
			newIntent = intent
			intentParam = ""
			intentParamValue = ""
			isParam = false
			intentParams = map[string]string{intentParam: intentParamValue}
		}
	}
	fmt.Println("Checking params for candidate intent " + intent)
	if strings.Contains(intent, "intent_photo_take_extend") {
		isParam = true
		newIntent = intent
		if strings.Contains(speechText, GetText(STR_ME)) || strings.Contains(speechText, GetText(STR_SELF)) {
			intentParam = "entity_photo_selfie"
			intentParamValue = "photo_selfie"
		} else {
			intentParam = "entity_photo_selfie"
			intentParamValue = ""
		}
		intentParams = map[string]string{intentParam: intentParamValue}
	} else if strings.Contains(intent, "intent_imperative_eyecolor") {
		isParam = true
		newIntent = "intent_imperative_eyecolor_specific_extend"
		intentParam = "eye_color"
		if strings.Contains(speechText, GetText(STR_EYE_COLOR_PURPLE)) {
			intentParamValue = "COLOR_PURPLE"
		} else if strings.Contains(speechText, GetText(STR_EYE_COLOR_BLUE)) || strings.Contains(speechText, GetText(STR_EYE_COLOR_SAPPHIRE)) {
			intentParamValue = "COLOR_BLUE"
		} else if strings.Contains(speechText, GetText(STR_EYE_COLOR_YELLOW)) {
			intentParamValue = "COLOR_YELLOW"
		} else if strings.Contains(speechText, GetText(STR_EYE_COLOR_TEAL)) || strings.Contains(speechText, GetText(STR_EYE_COLOR_TEAL2)) {
			intentParamValue = "COLOR_TEAL"
		} else if strings.Contains(speechText, GetText(STR_EYE_COLOR_GREEN)) {
			intentParamValue = "COLOR_GREEN"
		} else if strings.Contains(speechText, GetText(STR_EYE_COLOR_ORANGE)) {
			intentParamValue = "COLOR_ORANGE"
		} else {
			newIntent = intent
			intentParamValue = ""
			isParam = false
		}
		intentParams = map[string]string{intentParam: intentParamValue}
	} else if strings.Contains(intent, "intent_weather_extend") {
		isParam = true
		newIntent = intent
		go FetchWeatherNow(true)
		intentParams = map[string]string{"condition": string(currentCondition), "is_forecast": "false", "local_datetime": currentWeatherTime.String(), "speakable_location_string": currentLocation, "temperature": currentTemp, "temperature_unit": currentUnits}
	} else if strings.Contains(intent, "intent_imperative_volumelevel_extend") {
		isParam = true
		newIntent = intent
		if strings.Contains(speechText, GetText(STR_VOLUME_MEDIUM_LOW)) {
			intentParam = "volume_level"
			intentParamValue = "VOLUME_2"
		} else if strings.Contains(speechText, GetText(STR_VOLUME_LOW)) || strings.Contains(speechText, GetText(STR_VOLUME_QUIET)) {
			intentParam = "volume_level"
			intentParamValue = "VOLUME_1"
		} else if strings.Contains(speechText, GetText(STR_VOLUME_MEDIUM_HIGH)) {
			intentParam = "volume_level"
			intentParamValue = "VOLUME_4"
		} else if strings.Contains(speechText, GetText(STR_VOLUME_MEDIUM)) || strings.Contains(speechText, GetText(STR_VOLUME_NORMAL)) || strings.Contains(speechText, GetText(STR_VOLUME_REGULAR)) {
			intentParam = "volume_level"
			intentParamValue = "VOLUME_3"
		} else if strings.Contains(speechText, GetText(STR_VOLUME_HIGH)) || strings.Contains(speechText, GetText(STR_VOLUME_LOUD)) {
			intentParam = "volume_level"
			intentParamValue = "VOLUME_5"
		} else if strings.Contains(speechText, GetText(STR_VOLUME_MUTE)) || strings.Contains(speechText, GetText(STR_VOLUME_NOTHING)) || strings.Contains(speechText, GetText(STR_VOLUME_SILENT)) || strings.Contains(speechText, GetText(STR_VOLUME_OFF)) || strings.Contains(speechText, GetText(STR_VOLUME_ZERO)) {
			// there is no VOLUME_0 :(
			intentParam = "volume_level"
			intentParamValue = "VOLUME_1"
		} else {
			intentParam = "volume_level"
			intentParamValue = "VOLUME_1"
		}
		intentParams = map[string]string{intentParam: intentParamValue}
		// "my name is" is not possible anymore :/
		//}
		//else if strings.Contains(intent, "intent_names_username_extend") {
		// if !vars.VoskGrammerEnable {
		// 	var username string
		// 	var nameSplitter string = ""
		// 	isParam = true
		// 	newIntent = intent
		// 	if strings.Contains(speechText, GetText(STR_NAME_IS)) {
		// 		nameSplitter = GetText(STR_NAME_IS)
		// 	} else if strings.Contains(speechText, GetText(STR_NAME_IS2)) {
		// 		nameSplitter = GetText(STR_NAME_IS2)
		// 	} else if strings.Contains(speechText, GetText(STR_NAME_IS3)) {
		// 		nameSplitter = GetText(STR_NAME_IS3)
		// 	}
		// 	if nameSplitter != "" {
		// 		splitPhrase := strings.SplitAfter(speechText, nameSplitter)
		// 		username = strings.TrimSpace(splitPhrase[1])
		// 		if len(splitPhrase) == 3 {
		// 			username = username + " " + strings.TrimSpace(splitPhrase[2])
		// 		} else if len(splitPhrase) == 4 {
		// 			username = username + " " + strings.TrimSpace(splitPhrase[2]) + " " + strings.TrimSpace(splitPhrase[3])
		// 		} else if len(splitPhrase) > 4 {
		// 			username = username + " " + strings.TrimSpace(splitPhrase[2]) + " " + strings.TrimSpace(splitPhrase[3])
		// 		}
		// 		logger.Println("Name parsed from speech: " + "`" + username + "`")
		// 		intentParam = "username"
		// 		intentParamValue = username
		// 		intentParams = map[string]string{intentParam: intentParamValue}
		// 	} else {
		// 		logger.Println("No name parsed from speech")
		// 		intentParam = "username"
		// 		intentParamValue = ""
		// 		intentParams = map[string]string{intentParam: intentParamValue}
		// 	}
		// } else {
		// 	newIntent = "intent_system_noaudio"
		// }
	} else if strings.Contains(intent, "intent_clock_settimer_extend") {
		isParam = true
		newIntent = intent
		timerSecs := words2num(speechText)
		fmt.Println("Seconds parsed from speech: " + timerSecs)
		intentParam = "timer_duration"
		intentParamValue = timerSecs
		intentParams = map[string]string{intentParam: intentParamValue}
	} else if strings.Contains(intent, "intent_global_stop_extend") {
		isParam = true
		newIntent = intent
		intentParam = "what_to_stop"
		intentParamValue = "timer"
		intentParams = map[string]string{intentParam: intentParamValue}
	} else if strings.Contains(intent, "intent_message_playmessage_extend") {
		var given_name string
		isParam = true
		newIntent = intent
		intentParam = "given_name"
		if strings.Contains(speechText, GetText(STR_FOR)) {
			splitPhrase := strings.SplitAfter(speechText, GetText(STR_FOR))
			given_name = strings.TrimSpace(splitPhrase[1])
			if len(splitPhrase) == 3 {
				given_name = given_name + " " + strings.TrimSpace(splitPhrase[2])
			} else if len(splitPhrase) == 4 {
				given_name = given_name + " " + strings.TrimSpace(splitPhrase[2]) + " " + strings.TrimSpace(splitPhrase[3])
			} else if len(splitPhrase) > 4 {
				given_name = given_name + " " + strings.TrimSpace(splitPhrase[2]) + " " + strings.TrimSpace(splitPhrase[3])
			}
			intentParamValue = given_name
		} else {
			intentParamValue = ""
		}
		intentParams = map[string]string{intentParam: intentParamValue}
	} else if strings.Contains(intent, "intent_message_recordmessage_extend") {
		var given_name string
		isParam = true
		newIntent = intent
		intentParam = "given_name"
		if strings.Contains(speechText, GetText(STR_FOR)) {
			splitPhrase := strings.SplitAfter(speechText, GetText(STR_FOR))
			given_name = strings.TrimSpace(splitPhrase[1])
			if len(splitPhrase) == 3 {
				given_name = given_name + " " + strings.TrimSpace(splitPhrase[2])
			} else if len(splitPhrase) == 4 {
				given_name = given_name + " " + strings.TrimSpace(splitPhrase[2]) + " " + strings.TrimSpace(splitPhrase[3])
			} else if len(splitPhrase) > 4 {
				given_name = given_name + " " + strings.TrimSpace(splitPhrase[2]) + " " + strings.TrimSpace(splitPhrase[3])
			}
			intentParamValue = given_name
		} else {
			intentParamValue = ""
		}
		intentParams = map[string]string{intentParam: intentParamValue}
	} else {
		if intentParam == "" {
			newIntent = intent
			intentParam = ""
			intentParamValue = ""
			isParam = false
			intentParams = map[string]string{intentParam: intentParamValue}
		}
	}
	if botIsEarlyOpus {
		if strings.Contains(intent, "intent_imperative_praise") {
			isParam = false
			newIntent = "intent_imperative_affirmative"
			intentParam = ""
			intentParamValue = ""
			intentParams = map[string]string{intentParam: intentParamValue}
		} else if strings.Contains(intent, "intent_imperative_abuse") {
			isParam = false
			newIntent = "intent_imperative_negative"
			intentParam = ""
			intentParamValue = ""
			intentParams = map[string]string{intentParam: intentParamValue}
		} else if strings.Contains(intent, "intent_imperative_love") {
			isParam = false
			newIntent = "intent_greeting_hello"
			intentParam = ""
			intentParamValue = ""
			intentParams = map[string]string{intentParam: intentParamValue}
		}
	}
	return newIntent, intentParams, isParam
}

var textToNumber = map[string]int{
	GetText(STR_ZERO):        0,
	GetText(STR_ONE):         1,
	GetText(STR_TWO):         2,
	GetText(STR_THREE):       3,
	GetText(STR_FOUR):        4,
	GetText(STR_FIVE):        5,
	GetText(STR_SIX):         6,
	GetText(STR_SEVEN):       7,
	GetText(STR_EIGHT):       8,
	GetText(STR_NINE):        9,
	GetText(STR_TEN):         10,
	GetText(STR_ELEVEN):      11,
	GetText(STR_TWELVE):      12,
	GetText(STR_THIRTEEN):    13,
	GetText(STR_FOURTEEN):    14,
	GetText(STR_FIFTEEN):     15,
	GetText(STR_SIXTEEN):     16,
	GetText(STR_SEVENTEEN):   17,
	GetText(STR_EIGHTEEN):    18,
	GetText(STR_NINETEEN):    19,
	GetText(STR_TWENTY):      20,
	GetText(STR_THIRTY):      30,
	GetText(STR_FOURTY):      40,
	GetText(STR_FIFTY):       50,
	GetText(STR_SIXTY):       60,
	GetText(STR_SEVENTY):     70,
	GetText(STR_EIGHTY):      80,
	GetText(STR_NINETY):      90,
	GetText(STR_ONE_HUNDRED): 100,
}

func words2num(input string) string {

	initializeTextToNumberwithCurrentLocalization()
	totalSeconds := 0

	input = strings.ToLower(input)
	if strings.Contains(input, GetText(STR_ONE_HOUR)) || strings.Contains(input, GetText(STR_ONE_HOUR_ALT)) {
		return "3600"
	}

	str_regex_time_pattern := `(\d+|\w+(?:-\w+)?)\s*(` + GetText(STR_MINUTE) + `|` + GetText(STR_SECOND) + `|` + GetText(STR_HOUR) + `)s?`

	// timePattern := regexp.MustCompile(`(\d+|\w+(?:-\w+)?)\s*(minute|second|hour)s?`)
	timePattern := regexp.MustCompile(str_regex_time_pattern)

	matches := timePattern.FindAllStringSubmatch(input, -1)
	for _, match := range matches {
		unit := match[2]
		number := match[1]

		value, err := strconv.Atoi(number)
		if err != nil {
			value = mapTextToNumber(number)
		}

		switch unit {
		// minute
		case GetText(STR_MINUTE):
			totalSeconds += value * 60
		// second
		case GetText(STR_SECOND):
			totalSeconds += value
		// hour
		case GetText(STR_HOUR):
			totalSeconds += value * 3600
		}
	}

	return strconv.Itoa(totalSeconds)
}

func mapTextToNumber(text string) int {
	if val, ok := textToNumber[text]; ok {
		return val
	}
	parts := strings.Split(text, "-")
	sum := 0
	for _, part := range parts {
		if val, ok := textToNumber[part]; ok {
			sum += val
		}
	}
	return sum

}

func initializeTextToNumberwithCurrentLocalization() {
	textToNumber = map[string]int{
		GetText(STR_ZERO):        0,
		GetText(STR_ONE):         1,
		GetText(STR_TWO):         2,
		GetText(STR_THREE):       3,
		GetText(STR_FOUR):        4,
		GetText(STR_FIVE):        5,
		GetText(STR_SIX):         6,
		GetText(STR_SEVEN):       7,
		GetText(STR_EIGHT):       8,
		GetText(STR_NINE):        9,
		GetText(STR_TEN):         10,
		GetText(STR_ELEVEN):      11,
		GetText(STR_TWELVE):      12,
		GetText(STR_THIRTEEN):    13,
		GetText(STR_FOURTEEN):    14,
		GetText(STR_FIFTEEN):     15,
		GetText(STR_SIXTEEN):     16,
		GetText(STR_SEVENTEEN):   17,
		GetText(STR_EIGHTEEN):    18,
		GetText(STR_NINETEEN):    19,
		GetText(STR_TWENTY):      20,
		GetText(STR_THIRTY):      30,
		GetText(STR_FOURTY):      40,
		GetText(STR_FIFTY):       50,
		GetText(STR_SIXTY):       60,
		GetText(STR_SEVENTY):     70,
		GetText(STR_EIGHTY):      80,
		GetText(STR_NINETY):      90,
		GetText(STR_ONE_HUNDRED): 100,
	}
}
