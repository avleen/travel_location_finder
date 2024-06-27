// Find ticket prices between multiple sources and one destination.
// Config file is in YAML format and contains the rules and attendees.
// Additionally command line options for the destination is provided,
// along with the start, end dates and number of nights.
//
// We then use the google-flights-api/flights package to find the best
// offers for each source to the destination.
//
// Step 1: Parse the YAML from the config file conf.yml. The structure is as follows:
//		   rules:
//		       business_min_hrs: <int> # minimum hours for business class
//		       premium_min_hrs: <int> # minimum hours for premium economy class
//         attendees:
//			   <city name, string>: <int> # source city name, number of travelers
// Step 2: Create a new session with the google-flights-api/flights package
// Step 3: For each source, do a lookup 1 month from now. If the minimum
// 		   flight time is less than 6 hours, then we choose economy.
//         If the minimum flight time is between 6 and 10 hours, then we
//         choose premium economy. If the minimum flight time is over 10
//         hours, then we choose business.
// Step 4: Now do concurrent lookups for the sources to the destination, using
//         the options from step 3, the start and end dates, with the
//         GetPriceGraph() function.
// Step 5: Print the best offers for each source to the destination.
// Step 6: Close the session.
//
// Usage:
// 	$ cat sources.json | go run main.go --destination "Istanbul" --start-date "2025-02-02" --end-date "2025-02-07" --nights 6
//

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/krisukox/google-flights-api/flights"
	"golang.org/x/text/currency"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Rules struct {
		BusinessMinHrs int `yaml:"business_min_hrs"`
		PremiumMinHrs  int `yaml:"premium_min_hrs"`
	} `yaml:"rules"`
	Attendees []struct {
		City      string `yaml:"city"`
		Travelers int    `yaml:"travelers"`
	} `yaml:"attendees"`
}

func importConfig() Config {
	// Read conf.yml and return a config object
	var config Config
	// Read the config file
	f, err := os.Open("conf.yml")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	// Parse the config file
	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&config)
	if err != nil {
		log.Fatal(err)
	}
	// Return the config object
	return config
}
func lookup_flight_time(src, dst string, session *flights.Session) (time.Duration, error) {
	// Look up how long the shortest flight is between the source and destination
	// one month from now.
	options := flights.Options{
		Travelers: flights.Travelers{Adults: 1},
		Currency:  currency.USD,
		Stops:     flights.Stop2,
		Class:     flights.Economy,
		TripType:  flights.RoundTrip,
		Lang:      language.English,
	}

	offers, _, err := session.GetOffers(context.Background(), flights.Args{
		Date:       time.Now().AddDate(0, 1, 0),
		ReturnDate: time.Now().AddDate(0, 1, 7),
		SrcCities:  []string{src},
		DstCities:  []string{dst},
		Options:    options,
	})
	if err != nil {
		log.Fatal(err)
	}

	var bestDuration flights.FullOffer
	for _, offer := range offers {
		// Find the shortest flight time
		if offer.FlightDuration != 0 && (bestDuration.FlightDuration == 0 || offer.FlightDuration < bestDuration.FlightDuration) {
			bestDuration = offer
		}
	}

	return bestDuration.FlightDuration, nil
}

func Processor(wg *sync.WaitGroup, FlightRequirements chan flights.PriceGraphArgs, session *flights.Session) {
	// log.Println("Processor started")
	defer wg.Done()
	for Req := range FlightRequirements {
		src := Req.SrcCities[0]
		dst := Req.DstCities[0]
		// Get the shortest flight time between the source and destination
		// Choose the class based on the flight time
		class := GetFlightClass(src, dst, session)
		// log.Printf("Choosing class %v for flight from %s to %s", class, src, dst)
		// Set the class in the flight requirements
		Req.Options.Class = class
		// Get the best offers for the source to the destination
		priceGraphOffers, err := session.GetPriceGraph(context.Background(), Req)
		if err != nil {
			log.Fatal(err)
		}

		// Iterate over the offers and get the best offer
		var lg sync.WaitGroup
		lg.Add(len(priceGraphOffers))
		for _, priceGraphOffer := range priceGraphOffers {
			go GetActualOffers(&lg, session, priceGraphOffer, Req, src, dst)
		}
		lg.Wait()
	}
}

func GetActualOffers(lg *sync.WaitGroup, session *flights.Session, priceGraphOffer flights.Offer, Req flights.PriceGraphArgs, src string, dst string) {
	defer lg.Done()
	offers, _, err := session.GetOffers(context.Background(), flights.Args{
		Date:       priceGraphOffer.StartDate,
		ReturnDate: priceGraphOffer.ReturnDate,
		SrcCities:  Req.SrcCities,
		DstCities:  Req.DstCities,
		Options:    Req.Options,
	})
	if err != nil {
		log.Fatal(err)
	}
	var bestOffer flights.FullOffer
	for _, offer := range offers {
		if bestOffer.Price == 0 || offer.Price < bestOffer.Price {
			bestOffer = offer
		}
	}
	fmt.Println("Best offer for", src, "to", dst, "is", bestOffer.Price)
}

func GetFlightClass(src string, dst string, session *flights.Session) flights.Class {
	// log.Printf("Looking up flight class from %s to %s", src, dst)
	flight_time, err := lookup_flight_time(src, dst, session)
	if err != nil {
		log.Fatal(err)
	}
	// log.Printf("Shortest flight time from %s to %s is %v", src, dst, flight_time)

	var class flights.Class
	if flight_time < 6*time.Hour {
		class = flights.Economy
	} else if flight_time >= 6*time.Hour && flight_time < 10*time.Hour {
		class = flights.PremiumEconomy
	} else {
		class = flights.Business
	}

	return class
}

func main() {
	t := time.Now()
	// Parse command line arguments
	var destination, startDate, endDate string
	var nights int
	flag.StringVar(&destination, "destination", "", "The destination airport code")
	flag.StringVar(&startDate, "start-date", "", "The start date in the format 'YYYY-MM-DD'")
	flag.StringVar(&endDate, "end-date", "", "The end date in the format 'YYYY-MM-DD'")
	flag.IntVar(&nights, "nights", 0, "The number of nights")
	flag.Parse()

	if destination == "" || startDate == "" || endDate == "" || nights == 0 {
		log.Fatal("destination, start-date, end-date and nights are required")
	}

	config := importConfig()

	// Create a new session with the google-flights-api/flights package
	session, err := flights.New()
	if err != nil {
		log.Fatal(err)
	}

	// Convert start-date and end-date to time.Date
	startDateParsed, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		log.Fatal(err)
	}
	endDateParsed, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		log.Fatal(err)
	}

	// Create the base flights.PriceGraphArgs which are the same for all flights
	baseArgs := flights.PriceGraphArgs{
		RangeStartDate: startDateParsed,
		RangeEndDate:   endDateParsed,
		TripLength:     nights,
		DstCities:      []string{destination},
		Options: flights.Options{
			Currency: currency.USD,
			Stops:    flights.Stop2,
			TripType: flights.RoundTrip,
			Lang:     language.English,
		},
	}

	// Make a channel to put the flight requirements in
	FlightRequirements := make(chan flights.PriceGraphArgs)

	// For each source, add the source to the baseArgs and add it to the flight requirements channel
	var ProcessorWg sync.WaitGroup
	ProcessorWg.Add(len(config.Attendees))
	// var FlightClassWg sync.WaitGroup
	// FlightClassWg.Add(len(config.Attendees))
	for _, source := range config.Attendees {
		args := baseArgs
		args.SrcCities = []string{source.City}
		args.Options.Travelers = flights.Travelers{Adults: source.Travelers}
		go Processor(&ProcessorWg, FlightRequirements, session)
		FlightRequirements <- args
	}

	close(FlightRequirements)
	ProcessorWg.Wait()
	fmt.Println(time.Since(t))
}
