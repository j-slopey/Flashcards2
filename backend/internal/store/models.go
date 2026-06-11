package store

// Spanish holds the Spanish-bridge information for a card. Relation is one of
// "cognate", "false_friend", or "none". Word and Note are empty when there is
// no useful Spanish relation.
type Spanish struct {
	Relation string `json:"relation"`
	Word     string `json:"word"`
	Note     string `json:"note"`
}

// Card is a single (Italian word, part-of-speech sense) flashcard.
type Card struct {
	ID          int64   `json:"id"`
	Level       string  `json:"level"`
	Word        string  `json:"word"`
	POS         string  `json:"pos"`         // pos_category, e.g. "verb"
	POSDisplay  string  `json:"pos_display"` // raw dict abbrev, e.g. "v.tr."
	English     string  `json:"english"`
	Spanish     Spanish `json:"spanish"`
	OtherSenses int     `json:"other_senses"` // count of other cards sharing this word
}

// Level is a vocabulary level with the number of (translated) cards in it.
type Level struct {
	Level string `json:"level"`
	Count int    `json:"count"`
}

// SessionCard is one position in a study session: the card plus the user's
// self-graded result so far.
type SessionCard struct {
	Position int   `json:"position"`
	Card     Card  `json:"card"`
	Answered bool  `json:"answered"`
	Correct  *bool `json:"correct"` // nil until answered
}

// Session is a study session and its ordered cards.
type Session struct {
	ID        int64         `json:"id"`
	Levels    []string      `json:"levels"`
	Size      int           `json:"size"`
	Completed bool          `json:"completed"`
	Cards     []SessionCard `json:"cards"`
}

// Progress summarises how far through a session the user is.
type Progress struct {
	Answered  int  `json:"answered"`
	Correct   int  `json:"correct"`
	Total     int  `json:"total"`
	Completed bool `json:"completed"`
}
