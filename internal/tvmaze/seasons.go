package tvmaze

import (
	"context"
	"fmt"
	"slices"
)

// Seasons returns the show's numbered seasons in ascending order, in one
// request (the episode list would cost a far larger body).
func (c *Client) Seasons(ctx context.Context, id int) ([]int, error) {
	var raw []struct {
		Number *int `json:"number"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/shows/%d/seasons", id), &raw); err != nil {
		return nil, err
	}
	seasons := make([]int, 0, len(raw))
	for _, s := range raw {
		if s.Number != nil && *s.Number > 0 && !slices.Contains(seasons, *s.Number) {
			seasons = append(seasons, *s.Number)
		}
	}
	slices.Sort(seasons)
	return seasons, nil
}
