package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/araddon/dateparse"
	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/index/scorch"
	"github.com/dpapathanasiou/go-recaptcha"
	"github.com/gofiber/fiber"
)

var (
	app             *fiber.App
	index           bleve.Index
	applist         map[string]game
	incomingWebhook string
	outgoingWebhook string
	adminKey        string
	showcases       []showcase
)

type game struct {
	AppID int    `json:"appid"`
	Name  string `json:"name"`
}

type _ISteamAppsJSON struct {
	AppList struct {
		Apps []game
	}
}

type query struct {
	Query string `query:"q"`
}

type submission struct {
	AppID     string `json:"appid"`
	Recaptcha string `json:"recaptcha"`
}

type outgoing struct {
	AppID string `query:"appid"`
	Key   string `query:"key"`
}

type wh struct {
	Content string `json:"content"`
}

type showcase struct {
	// I don't know how this screws up, but for some reason there's some mangling of the appid field which ofc needs to be correct
	// so we'll specify the store page and capsule manually
	Store       string   `json:"store"`
	Capsule     string   `json:"capsule"`
	Name        string   `json:"name"`
	Snippet     string   `json:"snippet"`
	Tags        []string `json:"tags"`
	Price       string   `json:"price"`
	Percent     string   `json:"percent"`
	Developer   string   `json:"developer"`
	Publisher   string   `json:"publisher"`
	ReleaseYear string   `json:"release_year"`
	Platforms   string   `json:"platforms"`
}

type storefrontAPI struct {
	Success bool `json:"success"`
	Data    struct {
		Type             string   `json:"type"`
		Name             string   `json:"name"`
		ShortDescription string   `json:"short_description"`
		Developers       []string `json:"developers"`
		Publishers       []string `json:"publishers"`
		PriceOverview    struct {
			Currency        string `json:"currency"`
			Initial         int    `json:"initial"`
			Final           int    `json:"final"`
			DiscountPercent int    `json:"discount_percent"`
		} `json:"price_overview"`
		Genres []struct {
			ID          int    `json:"id"`
			Description string `json:"description"`
		} `json:"genres"`
		ReleaseDate struct {
			ComingSoon bool   `json:"coming_soon"`
			Date       string `json:"date"`
		} `json:"release_date"`
		Platforms struct {
			Windows bool `json:"windows"`
			Mac     bool `json:"mac"`
			Linux   bool `json:"linux"`
		} `json:"platforms"`
	} `json:"data"`
}

func handleStatus(ctx *fiber.Ctx, code int, message string) {
	ctx.SendStatus(code)
	ctx.SendString(message)
	return
}

func readOrPanic(fn string) []byte {
	file, err := ioutil.ReadFile(fn)
	if err != nil {
		panic(err)
	}

	return file
}

func fetchSteamApps() (*_ISteamAppsJSON, error) {
	resp, err := http.Get("https://api.steampowered.com/ISteamApps/GetAppList/v2/")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	file, err := ioutil.ReadAll(resp.Body)

	apps := &_ISteamAppsJSON{}

	if err := json.Unmarshal(file, apps); err != nil {
		return nil, err
	}

	return apps, nil
}

func commitShowcases() error {
	ibytes, err := json.Marshal(showcases)

	if err != nil {
		return err
	}

	ioutil.WriteFile("suggestions.json", ibytes, 0660)

	return nil
}

func main() {
	recaptcha.Init(string(readOrPanic("recaptcha.key")))
	incomingWebhook = string(readOrPanic("incoming_webhook.key"))
	outgoingWebhook = string(readOrPanic("outgoing_webhook.key"))
	adminKey = string(readOrPanic("admin.key"))

	apps, err := fetchSteamApps()
	if err != nil {
		panic(fmt.Sprintf("Could not fetch apps: %v", err))
	}

	applist := make(map[string]game)

	for _, val := range apps.AppList.Apps {
		applist[strconv.Itoa(val.AppID)] = val
	}

	if _, err := os.Stat("suggestions.json"); os.IsNotExist(err) {
		showcases = make([]showcase, 0)
		commitShowcases()
	} else {
		if err = json.Unmarshal(readOrPanic("suggestions.json"), &showcases); err != nil {
			panic(err)
		}
	}

	if _, err := os.Stat("map"); !os.IsNotExist(err) {
		index, err = bleve.Open("map")
		if err != nil {
			panic(fmt.Sprintf("Could not start Bleve: %v", err))
		}
	} else {
		index, err = bleve.NewUsing("map", bleve.NewIndexMapping(), scorch.Name, scorch.Name, nil)
		if err != nil {
			panic(fmt.Sprintf("Could not start Bleve: %v", err))
		}

		batch := index.NewBatch()

		fmt.Printf("Indexing %d items. This may take a long time.\n", len(apps.AppList.Apps))

		itime := int64(0)

		for i, val := range apps.AppList.Apps {
			if i%1000 == 0 {
				stime := int64(0)
				if itime > 0 {
					stime = time.Now().Unix() - itime
				}
				itime = time.Now().Unix()

				fmt.Printf("done %d/%d (last 1000 took %d s, ETA ~%d s)        \r", i, len(apps.AppList.Apps), stime, (stime * (int64(len(apps.AppList.Apps)-i) / 1000)))
			}
			//fmt.Printf("Indexing (%d/%d) [%d %s]                                                    \r", i, len(apps.AppList.Apps), val.AppID, val.Name)
			batch.Index(strconv.Itoa(val.AppID), val)
		}

		fmt.Printf("done %d/%d (finished)\n", len(apps.AppList.Apps), len(apps.AppList.Apps))
		fmt.Println("Committing")

		index.Batch(batch)
	}

	app = fiber.New()
	app.Get("/", func(ctx *fiber.Ctx) {
		bytes, err := ioutil.ReadFile("index.html")
		if err != nil {
			handleStatus(ctx, 500, "Error reading index")
			return
		}

		ctx.Set("Content-Type", "text/html")
		ctx.SendBytes(bytes)
	})

	app.Get("/api/search", func(ctx *fiber.Ctx) {
		qs := &query{}
		err := ctx.BodyParser(qs)
		if err != nil {
			handleStatus(ctx, 500, "Error reading request body")
			fmt.Println(err)
			return
		}

		q := bleve.NewMatchQuery(qs.Query)
		q.SetFuzziness(0)

		result, err := index.Search(bleve.NewSearchRequest(q))
		if err != nil {
			fmt.Println(err)
			handleStatus(ctx, 500, "Error searching")
			return
		}

		res := make([]game, len(result.Hits))

		for i, hit := range result.Hits {
			res[i] = applist[hit.ID]
		}

		j, err := json.Marshal(res)
		if err != nil {
			handleStatus(ctx, 500, "Error searching")
			return
		}

		ctx.Set("Content-Type", "application/json")
		ctx.SendBytes(j)
		return
	})

	app.Post("/api/submit", func(ctx *fiber.Ctx) {
		qs := &submission{}
		err := ctx.BodyParser(qs)

		if err != nil || qs.AppID == "" || qs.Recaptcha == "" {
			handleStatus(ctx, 400, "Invalid request")
			return
		}

		if _, has := applist[qs.AppID]; !has {
			handleStatus(ctx, 400, "Invalid AppID")
			return
		}

		if ok, err := recaptcha.Confirm(ctx.IPs()[len(ctx.IPs())-1], qs.Recaptcha); !ok || err != nil {
			handleStatus(ctx, 400, "Invalid Recaptcha")
			return
		}

		whJSON := []byte(`{"content": "https://store.steampowered.com/app/` + qs.AppID + `"}`)

		req, err := http.NewRequest("POST", incomingWebhook, bytes.NewBuffer(whJSON))
		if err != nil {
			handleStatus(ctx, 500, "Couldn't push incoming")
			return
		}

		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			handleStatus(ctx, 500, "Couldn't push incoming (http)")
			return
		}
		defer resp.Body.Close()

		handleStatus(ctx, 200, "Done")
	})

	app.Get("/api/outgoing", func(ctx *fiber.Ctx) {
		qs := outgoing{}
		err := ctx.BodyParser(&qs)

		if err != nil || qs.AppID == "" || qs.Key == "" {
			handleStatus(ctx, 400, "Invalid request")
			return
		}

		if qs.Key != adminKey {
			handleStatus(ctx, 400, "Nice try.")
			return
		}

		res, err := http.Get("https://store.steampowered.com/api/appdetails/?appids=" + qs.AppID + "&cc=us")
		if err != nil || res.StatusCode != 200 {
			fmt.Println(res.StatusCode)
			handleStatus(ctx, 500, "Could not fetch store page")
			return
		}
		defer res.Body.Close()

		sfapi := make(map[string]storefrontAPI)
		sfbytes, err := ioutil.ReadAll(res.Body)
		if err != nil {
			handleStatus(ctx, 500, "Error parsing Storefront API")
		}

		err = json.Unmarshal(sfbytes, &sfapi)
		if err != nil {
			handleStatus(ctx, 500, "Error parsing Storefront API")
		}

		storefront := sfapi[qs.AppID]

		tags := make([]string, 0)
		for _, cat := range storefront.Data.Genres {
			tags = append(tags, cat.Description)
		}

		year := ""
		dt, err := dateparse.ParseAny(storefront.Data.ReleaseDate.Date)
		if err != nil {
			fmt.Println(err)
			year = "(unknown)"
		} else {
			year = strconv.Itoa(dt.Year())
		}

		platforms := ""

		if storefront.Data.Platforms.Windows {
			platforms += "W"
		}

		if storefront.Data.Platforms.Mac {
			platforms += "M"
		}

		if storefront.Data.Platforms.Linux {
			platforms += "L"
		}

		dev := "(unknown)"
		if len(storefront.Data.Developers) > 0 {
			dev = storefront.Data.Developers[0]
		}

		pub := "(unknown)"
		if len(storefront.Data.Publishers) > 0 {
			pub = storefront.Data.Publishers[0]
		}

		sc := showcase{
			"https://store.steampowered.com/app/" + qs.AppID,
			"https://steamcdn-a.akamaihd.net/steam/apps/" + qs.AppID + "/header.jpg",
			applist[qs.AppID].Name,
			storefront.Data.ShortDescription,
			tags,
			"$" + fmt.Sprintf("%.2f", float64(storefront.Data.PriceOverview.Final)/100.0) + " " + storefront.Data.PriceOverview.Currency,
			"-" + strconv.Itoa(storefront.Data.PriceOverview.DiscountPercent) + "%",
			dev,
			pub,
			year,
			platforms}

		showcases = append(showcases, sc)
		commitShowcases()

		pubstring := ""
		if sc.Developer == sc.Publisher {
			pubstring = sc.Developer
		} else {
			pubstring = sc.Developer + ", " + sc.Publisher
		}

		hook := &wh{}
		hook.Content = "__**" + sc.Name + "** (" + sc.ReleaseYear + "; " + pubstring + "; " + sc.Platforms + ") **" + sc.Percent + "** " + sc.Price + "__\n" + sc.Snippet + " (" + strings.Join(tags, ", ") + ")\n<https://store.steampowered.com/app/" + qs.AppID + ">"

		hookJSON, err := json.Marshal(hook)
		if err != nil {
			handleStatus(ctx, 500, "Couldn't push outgoing")
			return
		}

		req, err := http.NewRequest("POST", outgoingWebhook, bytes.NewBuffer(hookJSON))
		if err != nil {
			handleStatus(ctx, 500, "Couldn't push outgoing")
			return
		}

		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			handleStatus(ctx, 500, "Couldn't push outgoing (http)")
			return
		}
		defer resp.Body.Close()

		byts, err := json.Marshal(sc)
		ctx.SendBytes(byts)
	})

	app.Get("/api/suggestions", func(ctx *fiber.Ctx) {
		ctx.Set("Content-Type", "application/json")

		xbytes, err := json.Marshal(showcases)

		if err != nil {
			handleStatus(ctx, 500, "Error")
			return
		}

		ctx.SendBytes(xbytes)
	})

	app.Listen(3000)
}
