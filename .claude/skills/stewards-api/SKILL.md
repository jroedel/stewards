---
name: stewards-api
description: Add plants, their photos and where they grow to the live garden steward app through its API, as the steward whose key is in STEWARDS_API_KEY. Use when the person asks to upload, import or add species data, species photos or a place's plants to the site — not for developing the app.
---

# Adding plants, photos and listings through the API

The garden steward app has a JSON API at `/api/v1` for exactly this: a
steward's own Claude adding plants and photos without typing them into the
screens. This skill is how to use it well. It writes to the **live site**
that volunteers read, so it is used only when the person has asked for an
upload, never while developing the app (CLAUDE.md §6) and never to test a
change.

## Start from the index, every time

```sh
scripts/stewards-api GET /api/v1
```

The index lists every endpoint, the fields each takes, the values a field may
have, and the rules. It is built from the binary's own route table, so it is
always the version that is running; trust it over this file. Read it before
the first call in a session.

## The key

`scripts/stewards-api` reads the key from `STEWARDS_API_KEY`, or else from
`~/.config/stewards/api-key` (mode 600), and hands it to curl without putting
it on a command line. The person makes a key at `/steward/keys` and saves it
in that file themselves. The file is the way for Claude Code, whose shell
keeps no variables between commands. It is never `secrets.env`, which is not
to be read at all (CLAUDE.md §6).

- Never print it, echo it, write it to a file, or put its value in a command.
  Refer to it only as `$STEWARDS_API_KEY`, and only through the script.
- Never read the key file, or look for a key anywhere else. If the script
  says there is no key, or the API answers 401, stop and ask the person to
  make one or save it.

## What the API will not do, and why that is right

- **It never confirms a plant or checks a photo.** A steward does that on the
  screens after looking. Do not send `confirmed` or `checked`, and do not
  describe an upload as confirmed. The skinny bed guide's two "frostweed"
  photos were wingstem; a source saying a name is not the same as the plant
  being that species.
- **It removes nothing.** Removing is a person's job on the screens.
- **It never says pull.** A listing (a plant at a place) has no box for a
  steward to tick first; it is on the place card the moment it is made. So
  an import may protect a plant or mark it careful, and only a steward on
  the place's Plants screen marks one to pull. Do not try to get round it
  with a note that says "pull": tell the person which plants you think are
  to be pulled, and let them do it.
- **A change to a confirmed plant clears its confirmation.** The answer says
  `"confirmation_cleared": true`. Before changing a confirmed plant, tell the
  person it will need confirming again.

## How to run a batch

1. **Read what is there:** `GET /api/v1/species` (and `/api/v1/places` for
   photo places). Match by scientific name as well as slug, so a plant that is
   already there under another address is not added twice.
2. **Draft locally and show the person** what you will send: a short table of
   plants (slug, common name, scientific name, what changes) and photos (plant,
   kind, source, credit). Wait for a yes. A batch to the live site is not
   something to start unasked.
3. **Send plants** with `PUT /api/v1/species/<slug>`, one JSON file each:

   ```sh
   scripts/stewards-api PUT /api/v1/species/winecup --json @batch/winecup.json
   ```

   The slug is permanent (it is printed on stakes): lower-case, hyphens,
   the common name, such as `winecup` or `brazos-penstemon`. Sizes are whole
   inches; bloom months are 1–12.
4. **Send photos** with `POST /api/v1/species/<slug>/photos`:

   ```sh
   scripts/stewards-api POST /api/v1/species/winecup/photos \
     -F photo=@batch/winecup-leaf.jpg -F kind=leaf -F source=borrowed \
     -F 'credit=A. Botanist' -F 'license=CC BY-SA 4.0' \
     -F 'source_url=https://commons.wikimedia.org/wiki/File:…'
   ```

5. **List plants at places** with
   `PUT /api/v1/places/<place>/plants/<species>`, once the plant exists:

   ```sh
   scripts/stewards-api PUT /api/v1/places/skinny-basement-bed/plants/pigeonberry \
     --json '{"action": "protect", "planned": true, "note": {"en": "6 plants, at the shady end"}}'
   ```

   `planned` is true when there is planting still to do: none of it is in
   the ground yet, or more is going in beside what is there. It puts the
   plant on the place's To plant list. It is false for anything already
   growing, whether planted last week or come up on its own. Ask the person
   which it is rather than guess: a bed planted a month ago is not "to
   plant". There is no count or zone field: put them in the note, and for
   "more going in" say how many ("6 in, 3 more by the wall").
   `GET /api/v1/places/<place>/plants` reads back what a place lists.
6. **Report** what was created, updated, unchanged and duplicate, and give the
   person the `photos_url` of each plant: that screen is where they check the
   photos, one by one, before any volunteer sees them.

Sending the same batch again is safe: an unchanged plant is `unchanged`, and a
photo already kept answers `"duplicate": true`.

## Plant data

- `sources` is what the ID was checked against. The Lady Bird Johnson
  Wildflower Center's page for the species is the usual first one; give its
  URL. Do not cite a source you did not read.
- `status`: `native`, `cultivar` (a native-derived cultivar or hybrid),
  `adapted`, `edible`, `invasive`.
- English only in `en`. Leave every `es` out: Spanish comes from a native
  speaker, never from machine translation (design.md).
- Notes are for someone standing in the sun with dirty hands: short, plain,
  practical.

## Photos

Each photo is one of five kinds, and the kind is what the card shows it as:
`young` (the seedling — what a weeder is deciding about), `leaf` (a close-up,
for telling it from a look-alike out of bloom), `flower` (a close-up),
`mature` (the grown plant at full size, in a garden, not a nursery pot) and
`winter` (how it looks in winter, or its seed head). Leaf and flower are
separate on purpose; a photo showing both is the one that shows the leaf
better.

- **Borrowed** photos come from iNaturalist or Wikimedia Commons. The
  stewards have decided CC0, CC BY, CC BY-SA and **CC BY-NC** are all fine
  (the site is a non-commercial ministry, which is the use BY-NC allows).
  Never ND, and never all-rights-reserved. Take the author, licence and the
  photo's page from the source's API, not from memory: on iNaturalist the
  photo's `license_code` and the observer's name, with
  `https://www.inaturalist.org/photos/<id>` as its page; on Commons
  `prop=imageinfo&iiprop=url|extmetadata` (`Artist`, `LicenseShortName`,
  `descriptionurl`).
- **Cultivars** get their own plant record when they look different from the
  species (a white autumn sage, a pink Turk's cap), with photos of that
  cultivar. A cultivar that looks like the species shares its record, and is
  named in the note.
- **Our own** photos are `source=ours`, with `place` as a slug from
  `/api/v1/places` when the person says where it was taken.
- **Never upload a photo in which a person can be recognised.** Volunteers are
  in some of the garden's own photos (CLAUDE.md, "Photos and people"); leave
  those for the person to decide about on the screen.
- Download borrowed photos into `photos/` (which is gitignored) before
  uploading, and keep the originals as the camera or the source saved them:
  the server makes the sizes it needs and strips location and camera details
  itself.

## When something is refused

Every refusal is `{"error": {"field": "…", "problem": "…"}}`, and the problem
is a sentence saying what to fix. Fix that field and send again. A refusal you
cannot fix from the sentence is worth stopping for and telling the person
about, rather than trying variations.
