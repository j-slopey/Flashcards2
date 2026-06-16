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
	POS         string  `json:"pos"` // per-sense English POS, e.g. "noun", "adjective"
	English     string  `json:"english"`
	Spanish     Spanish `json:"spanish"`
	OtherSenses int     `json:"other_senses"` // count of other cards sharing this word
}

// Intervals holds the human-readable "next review in …" estimate for each of
// the four FSRS ratings, previewed at the moment the card is served.
type Intervals struct {
	Again string `json:"again"`
	Hard  string `json:"hard"`
	Good  string `json:"good"`
	Easy  string `json:"easy"`
}

// SessionCard is one position in a study session: the card, the FSRS interval
// preview, whether it's a brand-new word, and the user's rating so far.
type SessionCard struct {
	Position int       `json:"position"`
	Card     Card      `json:"card"`
	IsNew    bool      `json:"is_new"`  // first time this word is being introduced
	Preview  Intervals `json:"preview"` // predicted next interval per rating
	Answered bool      `json:"answered"`
	Rating   *int      `json:"rating"` // 1..4 (Again/Hard/Good/Easy), nil until answered
}

// Level is a vocabulary level with the number of (translated) cards in it.
type Level struct {
	Level string `json:"level"`
	Count int    `json:"count"`
}

// Session is a study session and its ordered cards.
type Session struct {
	ID        int64         `json:"id"`
	Levels    []string      `json:"levels"`
	Completed bool          `json:"completed"`
	NewCount  int           `json:"new_count"`    // new cards introduced in this session
	DueCount  int           `json:"due_count"`    // due reviews in this session
	Cards     []SessionCard `json:"cards"`
}

// Progress summarises how far through a session the user is. "Remembered"
// counts ratings of Hard/Good/Easy (i.e. not Again).
type Progress struct {
	Answered   int  `json:"answered"`
	Remembered int  `json:"remembered"`
	Total      int  `json:"total"`
	Completed  bool `json:"completed"`
}

// Settings holds the per-user study configuration.
type Settings struct {
	NewCardsPerDay int `json:"new_cards_per_day"`
}
