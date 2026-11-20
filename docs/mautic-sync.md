# Hearth and Mautic

Hearth is the CRM. Mautic (marketing.example.com) sends the email and records what people do with it. `hearthctl mautic-sync` keeps
them in step, in both directions. It is a CronJob (`mauticSync` in the chart, every ten minutes) and the same command by hand.

```
hearthctl mautic-sync                     dry run: reads both sides, writes neither
hearthctl mautic-sync -write              do it
hearthctl mautic-sync -write -only ID,ID  only these Hearth contacts (a proof)
```

It needs `MAUTIC_URL` (the Service inside the cluster, `http://mautic.marketing.svc.cluster.local`) and an OAuth2 client of its own,
`MAUTIC_CLIENT_ID` and `MAUTIC_CLIENT_SECRET`, from the Secret `hearth-mautic`. That client is made once by
`secrets/access/mautic-api-client.sh` in the codex. It has to run inside the cluster: Mautic honours `X-Forwarded-Proto` only from its
trusted proxy range.

## Out: Hearth to Mautic

* Every contact with a usable email address becomes a Mautic contact, matched on that address, so one somebody already put in Mautic is
  adopted and not doubled. The Hearth id is kept in the Mautic field `hearth_id`.
* Its company becomes a Mautic company (name, website, address, `hearth_company_id`) and the contact is linked to it.
* The owner carries across when the Hearth owner's email is also a Mautic user. A contact with no owner of its own takes its company's.
* Segments, kept by Hearth (they have no filters, so editing membership in Mautic is overwritten): `Hearth: all contacts`,
  `Hearth type: <Type>` for each type on the contact's company, and `Hearth tag: <tag>` for each free tag. Matching Mautic tags
  (`hearth`, `hearth-type-client`, `hearth-tag-expo`) are put on the contact. Take a type away in Hearth and the contact leaves that segment.
* A contact sent before that is later deleted, or loses its address, leaves every segment and is tagged `hearth-removed`, so a campaign
  does not reach it. Nothing is ever deleted in Mautic.
* What was sent is hashed, so a run with nothing new makes no calls to Mautic.

## Back: Mautic to Hearth

Each of these is one note on the contact's timeline (and its company's), written by "Mautic", keyed on the Mautic row it came from so a
repeated or interrupted run adds nothing twice: email sent, email opened (first open), clicked a link in an email, submitted a form,
unsubscribed, address bounced, marked do-not-contact. Only contacts the sync sent have a Mautic id, so only they get notes.

## Opt-out, both ways, and it only goes one way

* A contact Mautic marks do-not-contact for email (an unsubscribe, a bounce, or a person marking it) is opted out in Hearth:
  `contacts.marketing_opt_out_at` and `marketing_opt_out_reason` (`unsubscribed`, `bounced`, `manual`). The contact page says so, and the
  API shows `marketing_opt_out`.
* A contact opted out in Hearth (`PATCH /v1/contacts/{id}` with `"marketing_opt_out": true`) gets the do-not-contact in Mautic on the next run.
* If Mautic ever loses a do-not-contact Hearth holds, the next run puts it back.
* Nothing clears an opt-out: not the sync, not the API (`false` answers 400), not the contact page. The person has to ask to be emailed
  again, and then a person clears it on purpose in the database.
* Mautic itself refuses to send to a do-not-contact contact, so the unsubscribe link in an email blocks the next send on its own.

## What it does not do

* It does not send email. Campaigns and emails are built and sent in Mautic.
* It does not create Hearth contacts from Mautic contacts or form submissions. A form submission from somebody Hearth does not know is
  left in Mautic.
* It does not touch EmberOps. The Hearth to EmberOps sync (`crm-sync`) reads contacts as before; the new columns are not in its field list.

## Tables

`mautic_links` (the Mautic id and what was last sent, per contact), `mautic_company_links`, `mautic_sync_state` (where each pull stopped).
