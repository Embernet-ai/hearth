# The phone app and your server

Hearth installs as an app on a phone from the server you opened it on. The app has a **Hearth server address** setting, so a company that
runs its own Hearth can point the app at it.

Open **Settings, Phone app, Hearth server address**, or **Use a different Hearth server** on the sign-in page, or go to `/connect`. Type
the address (`hearth.example.com`, with `https://` added for you, or a full address with a port for a server on your own network).

1. The phone asks that address for `/.well-known/hearth`. Every Hearth answers it with a few words that say what it is. If nothing
   answers, or what answers is not a Hearth, the page says so and nothing is saved.
2. When it answers, the address is remembered on this phone, and the phone opens that server's sign-in.
3. After that, `/login` on any server this phone opens goes straight to the server it remembers. **Use this server instead** on the connect page
   forgets the address, and `/login?local=1` signs in on the server you are on without forgetting it.

The check is made by the phone itself, so it proves the phone can reach the server, not only that the server exists. The connect page
holds nothing of any installation, needs no sign-in, and is the one page whose policy lets the browser reach another address to ask this.
