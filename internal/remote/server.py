# Database steps of beads-remote, run as root on the server with python3 and
# PyMySQL (python3-pymysql). Passwords are read from their files, never
# passed on a command line. Prints RESULT lines like server.sh.
import os
import sys

try:
    import pymysql
except ImportError:
    print("RESULT\tfail\tdatabase\tpython3-pymysql is not installed\tapt-get install python3-pymysql")
    sys.exit(1)

DB = os.environ["DB"]
APPLY = os.environ["ACTION"] == "provision"
SYSTEM = {"information_schema", "mysql", "performance_schema", "sys"}
EXPECTED_GRANTS = 2  # USAGE ON *.*, then everything ON `DB`.*
PRIVS = ("SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, REFERENCES, INDEX, ALTER, "
         "CREATE TEMPORARY TABLES, LOCK TABLES, EXECUTE, CREATE VIEW, SHOW VIEW, "
         "CREATE ROUTINE, ALTER ROUTINE, EVENT, TRIGGER")


def res(status, name, detail="", fix=""):
    print(f"RESULT\t{status}\t{name}\t{detail}\t{fix}")


def read(path):
    with open(path) as f:
        return f.read().strip()


def connect(user, pwfile, db=None):
    return pymysql.connect(host="127.0.0.1", port=3306, user=user, password=read(pwfile),
                           database=db, autocommit=True)


try:
    admin = connect(os.environ["ADMIN_USER"], os.environ["ADMIN_PWFILE"])
except Exception as e:  # noqa: BLE001 - reported, not raised
    res("fail", "database", f"admin login failed: {e}", "check ADMIN_USER and its password file on the server")
    sys.exit(1)
cur = admin.cursor()

cur.execute("SHOW DATABASES")
if DB in {r[0] for r in cur.fetchall()}:
    res("ok", f"database {DB}", "exists")
elif APPLY:
    cur.execute(f"CREATE DATABASE `{DB}`")
    res("ok", f"database {DB}", "created")
else:
    res("fail", f"database {DB}", "missing", "beads-remote server provision")
    sys.exit(1)

cur.execute("SELECT COUNT(*) FROM mysql.user WHERE User = %s AND Host = '%%'", (DB,))
if cur.fetchone()[0]:
    res("ok", f"MySQL user {DB}", "exists")
elif APPLY:
    # %% because PyMySQL applies %-formatting when a query has arguments.
    cur.execute(f"CREATE USER `{DB}`@`%%` IDENTIFIED BY %s", (read(os.environ["PWFILE"]),))
    res("ok", f"MySQL user {DB}", "created")
else:
    res("fail", f"MySQL user {DB}", "missing", "beads-remote server provision")
    sys.exit(1)

cur.execute(f"SHOW GRANTS FOR `{DB}`@`%`")
grants = [r[0] for r in cur.fetchall()]
scoped = [g for g in grants if f"ON `{DB}`.*" in g]
if len(grants) == EXPECTED_GRANTS and scoped:
    res("ok", "grants", f"USAGE, plus {DB}.* only")
elif APPLY and not [g for g in grants if "USAGE" not in g and f"ON `{DB}`.*" not in g]:
    cur.execute(f"GRANT USAGE ON *.* TO `{DB}`@`%`")
    cur.execute(f"GRANT {PRIVS} ON `{DB}`.* TO `{DB}`@`%`")
    res("ok", "grants", f"granted on {DB}.* only")
else:
    res("fail", "grants", "unexpected: " + " | ".join(grants), "review by hand; revoke anything beyond the database")
    sys.exit(1)

try:
    mine = connect(DB, os.environ["PWFILE"])
except Exception as e:  # noqa: BLE001
    res("fail", "login as " + DB, f"{e}", "the password file and the MySQL password differ; ALTER USER to match the file")
    sys.exit(1)
c2 = mine.cursor()
c2.execute("SHOW DATABASES")
visible = {r[0] for r in c2.fetchall()} - SYSTEM
if visible == {DB}:
    res("ok", "isolation", f"{DB} sees only its own database")
else:
    res("fail", "isolation", f"{DB} can see: {', '.join(sorted(visible - {DB}))}", "revoke the extra grants")
    sys.exit(1)

# Dolt lets any login read and write server files with LOAD_FILE and
# SELECT ... INTO OUTFILE, FILE grant or not, unless secure_file_priv names a
# directory. Empty (Dolt's default) would let every project's developer read
# the others' data and the server's privilege store. A directory that exists
# still allows both inside it, and /var/lib/dolt would expose every database,
# so only NULL (both disabled) or a path that does not exist passes.
FIX = ("add 'system_variables: {secure_file_priv: /nonexistent-beads}' to the Dolt config and restart Dolt; "
       "deploy/bootstrap.sh does this")


def reads(cursor, path):
    """True when LOAD_FILE returned the file's contents."""
    try:
        cursor.execute("SELECT LOAD_FILE(%s)", (path,))
        return cursor.fetchone()[0] is not None
    except pymysql.MySQLError:
        return False


def writes(cursor, path):
    """True when SELECT ... INTO OUTFILE succeeded."""
    try:
        cursor.execute(f"SELECT 1 INTO OUTFILE '{path}'")
        return True
    except pymysql.MySQLError:
        return False


cur.execute("SELECT @@global.secure_file_priv")
priv = cur.fetchone()[0]
if priv == "":
    res("fail", "file access", f"{DB} can read and write any file the Dolt server can (secure_file_priv is empty)", FIX)
    sys.exit(1)
if priv is not None and os.path.exists(priv):
    res("fail", "file access", f"{DB} can read and write files in {os.path.realpath(priv)} (secure_file_priv names a directory that exists)", FIX)
    sys.exit(1)
probe_file = f"/tmp/beads-remote-probe-{os.getpid()}"
if reads(c2, "/etc/passwd") or writes(c2, probe_file):
    res("fail", "file access", f"{DB} reached the server's files despite secure_file_priv {priv!r}", FIX)
    sys.exit(1)
res("ok", "file access", "none (secure_file_priv " + ("NULL" if priv is None else priv) + ")")
