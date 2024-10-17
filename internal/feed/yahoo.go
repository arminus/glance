package feed

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

type marketResponseJson struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Currency           string  `json:"currency"`
				Symbol             string  `json:"symbol"`
				RegularMarketPrice float64 `json:"regularMarketPrice"`
				ChartPreviousClose float64 `json:"chartPreviousClose"`
			} `json:"meta"`
			Indicators struct {
				Quote []struct {
					Close []float64 `json:"close,omitempty"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
	} `json:"chart"`
}

// TODO: allow changing chart time frame
const marketChartDays = 21

func FetchUSDExchangeRate(currency string) (float64, error) {
	url := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/USD%s=X?range=1d&interval=1d", currency)
	resp, err := http.Get(url)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch exchange rate: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var response marketResponseJson
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(response.Chart.Result) == 0 {
		return 0, fmt.Errorf("no result in response")
	}

	return response.Chart.Result[0].Meta.RegularMarketPrice, nil
}

func FetchMarketsDataFromYahoo(marketRequests []MarketRequest) (Markets, error) {
	requests := make([]*http.Request, 0, len(marketRequests))

	for i := range marketRequests {
		request, _ := http.NewRequest("GET", fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s?range=1mo&interval=1d", marketRequests[i].Symbol), nil)
		requests = append(requests, request)
	}

	job := newJob(decodeJsonFromRequestTask[marketResponseJson](defaultClient), requests)
	responses, errs, err := workerPoolDo(job)

	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoContent, err)
	}

	markets := make(Markets, 0, len(responses))
	var failed int

	for i := range responses {
		if errs[i] != nil {
			failed++
			slog.Error("Failed to fetch market data", "symbol", marketRequests[i].Symbol, "error", errs[i])
			continue
		}

		response := responses[i]

		if len(response.Chart.Result) == 0 {
			failed++
			slog.Error("Market response contains no data", "symbol", marketRequests[i].Symbol)
			continue
		}

		prices := response.Chart.Result[0].Indicators.Quote[0].Close

		if len(prices) > marketChartDays {
			prices = prices[len(prices)-marketChartDays:]
		}

		previous := response.Chart.Result[0].Meta.RegularMarketPrice

		if len(prices) >= 2 && prices[len(prices)-2] != 0 {
			previous = prices[len(prices)-2]
		}

		points := SvgPolylineCoordsFromYValues(100, 50, maybeCopySliceWithoutZeroValues(prices))

		currency, exists := currencyToSymbol[response.Chart.Result[0].Meta.Currency]

		if !exists {
			currency = response.Chart.Result[0].Meta.Currency
		}

		if marketRequests[i].Currency != "" {
			exchangeRate, err := FetchUSDExchangeRate(marketRequests[i].Currency)
			if err != nil {
				slog.Error("Failed to fetch USD exchange rate", "error", err)
				continue
			}

			if response.Chart.Result[0].Meta.Currency == "USD" {
				response.Chart.Result[0].Meta.RegularMarketPrice *= exchangeRate
				previous *= exchangeRate
				currency = currencyToSymbol[marketRequests[i].Currency]
			}
		}

		markets = append(markets, Market{
			MarketRequest: marketRequests[i],
			Price:         response.Chart.Result[0].Meta.RegularMarketPrice,
			Currency:      currency,
			PercentChange: percentChange(
				response.Chart.Result[0].Meta.RegularMarketPrice,
				previous,
			),
			SvgChartPoints: points,
		})
	}

	if len(markets) == 0 {
		return nil, ErrNoContent
	}

	if failed > 0 {
		return markets, fmt.Errorf("%w: could not fetch data for %d market(s)", ErrPartialContent, failed)
	}

	return markets, nil
}
