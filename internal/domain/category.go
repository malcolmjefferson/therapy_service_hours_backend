package domain

// Category is one service category from the lookup table (CLAUDE.md §8).
type Category struct {
	ID    string
	Name  string
	Group CategoryGroup
}

// CategorySection is the categories of one group, in display order.
type CategorySection struct {
	Group CategoryGroup
	// Label is the display name of the group, for example "Direct Service".
	Label      string
	Categories []Category
}
