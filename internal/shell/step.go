package shell

import "strings"

// Step is one command of a privileged procedure, held as arguments rather than
// text so grove can run it without a shell and print it for one from the same
// value. Two copies of the procedure, one to show and one to run, is how the
// pasted version and the executed version come to differ.
type Step []string

// Quoted only where a shell would otherwise split or expand the word, so the
// common line reads as typed and the state directory with a space in it still
// arrives as one argument.
func (s Step) String() string {
	words := make([]string, len(s))
	for i, word := range s {
		if needsQuoting(word) {
			word = Quote(word)
		}
		words[i] = word
	}
	return strings.Join(words, " ")
}

func needsQuoting(word string) bool {
	if word == "" {
		return true
	}
	for _, r := range word {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-_./:=,+@%", r):
		default:
			return true
		}
	}
	return false
}
