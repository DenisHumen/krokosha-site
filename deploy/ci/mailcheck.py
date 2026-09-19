#!/usr/bin/env python3
"""Checks of the mail server for the installer's test (deploy/ci/test-install.sh).

Each command talks to the server the way a mail program or another mail server would, and exits
with 0 when the server behaved as it should. Nothing here ever runs on a real server.

    mailcheck.py login    USER PASSWORD               IMAP over TLS accepts the password
    mailcheck.py nologin  USER PASSWORD               …and refuses a wrong one
    mailcheck.py send     USER PASSWORD FROM TO TEXT  submission (587, STARTTLS) delivers a letter
    mailcheck.py spoof    USER PASSWORD FROM TO       …but not under somebody else's address
    mailcheck.py relay    FROM TO                     port 25 relays nothing for strangers
    mailcheck.py find     USER PASSWORD TEXT          a letter containing TEXT is in the INBOX
"""
import imaplib
import smtplib
import ssl
import sys
import time

HOST = "127.0.0.1"
# The test server has a self-signed certificate: encryption yes, trust no.
TLS = ssl.create_default_context()
TLS.check_hostname = False
TLS.verify_mode = ssl.CERT_NONE


def imap(user, password):
    box = imaplib.IMAP4_SSL(HOST, 993, ssl_context=TLS)
    box.login(user, password)
    return box


def submission(user, password):
    server = smtplib.SMTP(HOST, 587, timeout=20)
    server.starttls(context=TLS)
    server.login(user, password)
    return server


def letter(sender, recipient, text):
    return ("From: %s\r\nTo: %s\r\nSubject: mailcheck\r\nMessage-ID: <%s@mailcheck>\r\n\r\n%s\r\n"
            % (sender, recipient, time.time(), text)).encode("utf-8")


def main(argv):
    command, args = argv[1], argv[2:]
    if command == "login":
        imap(*args).logout()
        return 0
    if command == "nologin":
        try:
            imap(*args).logout()
        except imaplib.IMAP4.error:
            return 0
        print("a wrong password was accepted")
        return 1
    if command == "send":
        user, password, sender, recipient, text = args
        with submission(user, password) as server:
            server.sendmail(sender, [recipient], letter(sender, recipient, text))
        return 0
    if command == "spoof":
        user, password, sender, recipient = args
        try:
            with submission(user, password) as server:
                server.sendmail(sender, [recipient], letter(sender, recipient, "spoofed"))
        except (smtplib.SMTPSenderRefused, smtplib.SMTPRecipientsRefused, smtplib.SMTPDataError) as refusal:
            print("refused:", refusal)
            return 0
        print("%s was allowed to send as %s" % (user, sender))
        return 1
    if command == "relay":
        sender, recipient = args
        try:
            with smtplib.SMTP(HOST, 25, timeout=20) as server:
                server.sendmail(sender, [recipient], letter(sender, recipient, "relayed"))
        except (smtplib.SMTPSenderRefused, smtplib.SMTPRecipientsRefused, smtplib.SMTPDataError) as refusal:
            print("refused:", refusal)
            return 0
        print("the server relays mail for strangers")
        return 1
    if command == "find":
        user, password, text = args
        deadline = time.time() + 60
        while time.time() < deadline:
            box = imap(user, password)
            box.select("INBOX", readonly=True)
            _, found = box.search(None, "TEXT", '"%s"' % text)
            box.logout()
            if found and found[0].split():
                return 0
            time.sleep(3)
        print("no letter with %r in the INBOX of %s" % (text, user))
        return 1
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
