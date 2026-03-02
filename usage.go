package modelstore

import (
	"fmt"
	"time"
)

// TrackUsage records token usage for an agent/model.
func (s *Store) TrackUsage(agent, model string, inputTokens, outputTokens int64) error {
	// Resolve provider from model
	m, err := s.ResolveModel(model)
	provider := ""
	costUSD := 0.0
	if err == nil {
		provider = m.Provider
		costUSD = float64(inputTokens)/1_000_000*m.InputCost + float64(outputTokens)/1_000_000*m.OutputCost
	}

	date := time.Now().Format("2006-01-02")
	_, err = s.db.Exec(`
		INSERT INTO usage (date, agent, model, provider, input_tokens, output_tokens, requests, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT(date, agent, model) DO UPDATE SET
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			requests = requests + 1,
			cost_usd = cost_usd + excluded.cost_usd
	`, date, agent, model, provider, inputTokens, outputTokens, costUSD)
	return err
}

// Usage returns usage records with optional filters.
func (s *Store) Usage(agent, date string) ([]UsageRecord, error) {
	query := `SELECT date, agent, model, provider, input_tokens, output_tokens, requests, cost_usd FROM usage WHERE 1=1`
	var args []interface{}

	if agent != "" {
		query += ` AND agent = ?`
		args = append(args, agent)
	}
	if date != "" {
		query += ` AND date = ?`
		args = append(args, date)
	}

	query += ` ORDER BY date DESC, agent, model`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []UsageRecord
	for rows.Next() {
		var r UsageRecord
		if err := rows.Scan(&r.Date, &r.Agent, &r.Model, &r.Provider, &r.InputTokens, &r.OutputTokens, &r.Requests, &r.CostUSD); err != nil {
			continue
		}
		records = append(records, r)
	}
	return records, nil
}

// UsageSummary returns aggregated usage for a date range.
func (s *Store) UsageSummary(from, to string) ([]UsageRecord, error) {
	rows, err := s.db.Query(`
		SELECT date, agent, '' as model, '' as provider,
			SUM(input_tokens) as input_tokens, SUM(output_tokens) as output_tokens,
			SUM(requests) as requests, SUM(cost_usd) as cost_usd
		FROM usage
		WHERE date >= ? AND date <= ?
		GROUP BY date, agent
		ORDER BY date DESC, agent
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []UsageRecord
	for rows.Next() {
		var r UsageRecord
		if err := rows.Scan(&r.Date, &r.Agent, &r.Model, &r.Provider, &r.InputTokens, &r.OutputTokens, &r.Requests, &r.CostUSD); err != nil {
			continue
		}
		records = append(records, r)
	}
	return records, nil
}

// TotalCost returns total cost for a date range, optionally filtered by agent.
func (s *Store) TotalCost(from, to, agent string) (float64, error) {
	query := `SELECT COALESCE(SUM(cost_usd), 0) FROM usage WHERE date >= ? AND date <= ?`
	args := []interface{}{from, to}
	if agent != "" {
		query += ` AND agent = ?`
		args = append(args, agent)
	}
	var total float64
	err := s.db.QueryRow(query, args...).Scan(&total)
	return total, err
}

// PrintUsage formats usage records for display.
func PrintUsage(records []UsageRecord) string {
	if len(records) == 0 {
		return "No usage recorded."
	}

	result := fmt.Sprintf("%-12s %-14s %-30s %10s %10s %6s %8s\n",
		"Date", "Agent", "Model", "In", "Out", "Reqs", "Cost")
	result += "────────────────────────────────────────────────────────────────────────────────────────────\n"

	var totalIn, totalOut int64
	var totalCost float64
	var totalReqs int

	for _, r := range records {
		model := r.Model
		if model == "" {
			model = "(all)"
		}
		result += fmt.Sprintf("%-12s %-14s %-30s %10d %10d %6d %7s\n",
			r.Date, r.Agent, model, r.InputTokens, r.OutputTokens, r.Requests,
			fmt.Sprintf("$%.2f", r.CostUSD))
		totalIn += r.InputTokens
		totalOut += r.OutputTokens
		totalCost += r.CostUSD
		totalReqs += r.Requests
	}

	result += "────────────────────────────────────────────────────────────────────────────────────────────\n"
	result += fmt.Sprintf("%-12s %-14s %-30s %10d %10d %6d %7s\n",
		"", "TOTAL", "", totalIn, totalOut, totalReqs, fmt.Sprintf("$%.2f", totalCost))

	return result
}
