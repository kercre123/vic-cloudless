package vtr

import "strings"

var NumbersEN_US []string = []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety", "hundred", "seconds", "minutes", "hours", "minute", "second", "hour"}

func removeDuplicates(strings []string) []string {
	occurred := map[string]bool{}
	var result []string
	for _, str := range strings {
		if !occurred[str] {
			result = append(result, str)
			occurred[str] = true
		}
	}
	return result
}
func GetGrammerList(lang string) string {
	var wordsList []string
	var grammer string
	// add words in intent json
	// for _, words := range IntentList {
	// 	for _, word := range words.Keyphrases {
	// 		wors := strings.Split(word, " ")
	// 		for _, wor := range wors {
	// 			found := model.FindWord(wor)
	// 			if found != -1 {
	// 				wordsList = append(wordsList, wor)
	// 			}
	// 		}
	// 	}
	// }
	wordsList = append(wordsList, []string{"photo", "picture", "hello", "my name is", "whats", "weather", "my name", "what is", "set your", "eye color", "volume", "to", "how", "old", "are you", "start", "stop", "exploring", "go", "home", "to sleep", "good", "robot", "never", "mind", "morning", "afternoon", "evening", "night", "bye", "goodbye", "happy", "new year", "holidays", "sign in to alexa", "sign out of alexa", "move", "forward", "backwards", "turn", "around", "left", "right", "do", "pop", "a wheelie", "a wheelstand", "fistbump", "give me", "blackjack", "play", "yes", "no", "bad", "i'm", "i am", "sorry", "down", "up", "medium", "low", "high", "look at me", "shut", "love", "you", "have", "a", "question", "check", "the", "timer", "for", "what", "it", "time", "dance", "hit", "stand"}...)
	// add words in localization
	for _, str := range ALL_STR {
		text := GetText(str)
		wors := strings.Split(text, " ")
		for _, wor := range wors {
			if model == nil || model.FindWord(wor) != -1 {
				wordsList = append(wordsList, wor)
			}
		}
	}
	// add numbers
	for _, wor := range NumbersEN_US {
		if model == nil || model.FindWord(wor) != -1 {
			wordsList = append(wordsList, wor)
		}
	}

	wordsList = removeDuplicates(wordsList)
	for i, word := range wordsList {
		if i == len(wordsList)-1 {
			grammer = grammer + `"` + word + `"`
		} else {
			grammer = grammer + `"` + word + `"` + ", "
		}
	}
	grammer = "[" + grammer + "]"
	return grammer
}
