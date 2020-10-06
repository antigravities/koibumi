# koibumi

This is the code that ran the /r/Steam recommendation site for the Summer Sale 2020. The new recommendations site was rewritten from scratch and is called "Level Up" - that will be released as free software soon<sup>TM</sup>

I wrote this really quickly while I was still kind of learning how to Go and I can confidently say it's far from my best work. I don't even particularly remember how to run it, but here should be a good start? Use the source if you're confused.

1. Install Go 1.14+
2. `git clone git@github.com:antigravities/koibumi.git`
3. `cd koibumi && go build -i`
4. Create `recaptcha.key`, `incoming_webhook.key`, `outcoming_webhook.key`, and `admin.key` as appropriate
5. Edit index.html with your recaptcha site key
6. `./koibumi`

Licensed under the GNU AGPL v3.
