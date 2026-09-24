---
title: Privacy policy
updated: TODO   # publication date
status: draft   # DRAFT based on brief B5/B10 — legal review and a check against the real implementation before publishing
---

## In short

This site doesn't use cookies for analytics, doesn't use third-party trackers and doesn't sell data. Personal data appears only when you send a request through the form or sign in to the personal account yourself.

## Who processes the data

Denis (Krokosha), owner of krokosha.com. Contact for data questions: denis@krokosha.com.

## Anonymous visit statistics

To understand which sections are useful, the site collects anonymous statistics with its own open script, `/assets/analytics.js`:

- pages and sections viewed, scroll depth, time on page;
- clicks on contact buttons and social links;
- traffic source (referrer, ad UTM tags);
- device type, browser and OS — in general terms;
- country and city — derived from the IP address on the server; the IP itself is stored only in truncated form.

Visitors are distinguished by a hash of the IP address and browser with a salt that changes every day. It can't be used to identify you or to link visits across different days.

Not collected: keystrokes, form input, session recordings, browser fingerprints.

If **Do Not Track** or **Global Privacy Control** is enabled in your browser, no statistics are sent at all.

Detailed page-view records are kept for 12 months and then deleted; only daily totals remain (how many visits, from which countries, which sections were viewed) — nothing in them relates to an individual visitor.

## Server logs

Like any web server, this one keeps a technical request log (address, time, requested page, browser). Logs are used for security and diagnostics and are kept for 30 days.

## Requests sent through the form

When you send the "Discuss a project" form, what you entered is stored: name, contact method, area, task description, budget and timeline (if provided), attachments (if the form accepts them).

Anonymous information about the visit is attached to the request: where you came from, country, device type and which sections of the site you viewed before sending. This helps understand the context of the task faster.

The request is available only to Denis and to people Denis has personally given access to work with requests. Notifications about it are delivered to Telegram and email.

If the conversation continues — you reply to an email or write to the bot in Telegram — your messages and the files you attach are stored with the request. An email that cannot be matched to any request is kept for no longer than 30 days.

If you are signed in to the personal account, the request appears there: you can see its status and the whole conversation, and reply.

Requests and correspondence are kept for 24 months, then deleted or anonymized.

## Personal account

The account is optional: a request can be sent without it. It is created at the first sign-in — with a one-time code from an email or from the site's Telegram bot; there are no passwords.

What is stored is what you entered or what signing in needs: your email address and/or your Telegram — the account number and username; name, company, the language of letters and the preferred way to reach you; the contacts and social networks you added yourself — Denis sees them to get in touch with you; the devices you signed in from — browser and system in general terms, a truncated IP address, the time of the last visit; the easter eggs you found and the achievements for orders; a personal discount, if one is set.

The account's cookies are strictly necessary ones only:

- `__Host-kc` — the sign-in session: up to 90 days, ending sooner if you have not visited for 30 days or signed out;
- `__Host-kl` — for 15 minutes while you type a code: a code works only in the browser that asked for it.

The codes and sign-in links themselves are not stored: the server keeps a random number they can be checked against.

Discounts and the level of a regular client are calculated from your requests: the number of completed orders and their total (Denis enters it when an order is completed). The discount is fixed on a request when it is sent. Achievements for orders give no discounts.

You can delete the account in the account itself: the account, contacts, devices and achievements are deleted at once. Requests stay for their storage period; they can be deleted sooner on request. An account nobody signed in to for the whole storage period of requests, with no requests left in it, is deleted by itself.

## Easter eggs and achievements

The site has hidden easter eggs. The ones you find are kept in your browser (localStorage): the list of finds, receipts of them signed by the server (the receipt of "every egg" gives a one-time discount on a request) and service notes — whether the browser was counted as a player, which request the discount went to, whether you signed in to the account. Until you find something, nothing is written to your browser.

To show rarity the way Steam does — the share of players who found each egg — the server counts how many browsers found at least one egg and how many times each one was found. Only daily numbers are stored, without addresses or identifiers; against abuse, a hash of the address with the site's secret is used and lives no longer than an hour. With Do Not Track or Global Privacy Control enabled, finds are not counted (receipts are still issued); robots and automated browsers are not counted either.

If you are signed in to the account, the eggs you find are kept there too — so they are available on your other devices.

## Advertising

<!-- Q8: if flags.ads.pixels is enabled, rewrite this section for the actual pixels. -->
Advertising pixels (Google Ads, Meta) are not used on this site at the moment. If that changes, they will be described here and will load only after you give consent in the banner.

## Your rights

You can request a copy of your data, its correction or deletion — email denis@krokosha.com. Requests are handled within a reasonable time; deletion covers the request, correspondence and attachments.

## Changes

When the policy changes, the date at the top of the page is updated.
