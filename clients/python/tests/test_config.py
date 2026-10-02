import pytest

from truvity_cnpg import CnpgConfig, ConfigError, RetryPolicy, connection_params, parse_duration

BASE = {
    "PGHOST": "db-rw.ns.svc",
    "PGDATABASE": "app",
    "PGUSER": "app_role",
    "PGSSLROOTCERT": "/certs/ca.crt",
}


def test_defaults_follow_the_contract() -> None:
    c = CnpgConfig.from_env(BASE)
    assert c.port == 5432
    assert c.connect_timeout == 5
    assert c.statement_timeout == 30
    assert c.idle_in_tx_timeout == 60
    assert (c.pool_max, c.pool_min) == (10, 0)
    assert c.max_conn_lifetime == 30 * 60
    assert c.max_conn_idle == 5 * 60
    assert c.health_period == 30
    assert c.retry == RetryPolicy(5, 0.2, 5.0, 30.0)


def test_every_cnpg_client_variable_is_read() -> None:
    c = CnpgConfig.from_env(
        {
            **BASE,
            "PGPORT": "6432",
            "PGAPPNAME": "orders",
            "PGCONNECT_TIMEOUT": "3",
            "PGSSLCERT": "/certs/tls.crt",
            "PGSSLKEY": "/certs/tls.key",
            "CNPG_CLIENT_PASSWORD_FILE": "/pw",
            "CNPG_CLIENT_STATEMENT_TIMEOUT": "500ms",
            "CNPG_CLIENT_IDLE_TX_TIMEOUT": "0",
            "CNPG_CLIENT_POOL_MAX": "4",
            "CNPG_CLIENT_POOL_MIN": "1",
            "CNPG_CLIENT_CONN_MAX_LIFETIME": "10m",
            "CNPG_CLIENT_CONN_MAX_IDLE": "1m",
            "CNPG_CLIENT_HEALTH_PERIOD": "0",
            "CNPG_CLIENT_RETRY_ATTEMPTS": "7",
            "CNPG_CLIENT_RETRY_MAX_DELAY": "2s",
            "CNPG_CLIENT_RETRY_BUDGET": "1m",
        }
    )
    assert c.port == 6432
    assert c.application_name == "orders"
    assert c.connect_timeout == 3
    assert c.statement_timeout == 0.5
    assert c.idle_in_tx_timeout == 0
    assert (c.pool_max, c.pool_min) == (4, 1)
    assert c.max_conn_lifetime == 600
    assert c.max_conn_idle == 60
    assert c.health_period == 0
    assert c.retry == RetryPolicy(7, 0.2, 2.0, 60.0)
    assert c.password_file == "/pw"


def test_no_ca_a_weaker_mode_and_a_lone_certificate_are_reported_together() -> None:
    with pytest.raises(ConfigError) as e:
        CnpgConfig.from_env(
            {"PGHOST": "h", "PGDATABASE": "d", "PGUSER": "u", "PGSSLMODE": "require", "PGSSLCERT": "/c"}
        )
    assert len(e.value.problems) == 3, str(e.value)
    text = "\n".join(e.value.problems)
    assert "only verify-full is accepted" in text
    assert "server CA file is required" in text
    assert "go together" in text


@pytest.mark.parametrize("mode", ["disable", "allow", "prefer", "require", "verify-ca"])
def test_verify_full_is_the_one_accepted_mode(mode: str) -> None:
    CnpgConfig.from_env({**BASE, "PGSSLMODE": "verify-full"})
    with pytest.raises(ConfigError, match="only verify-full is accepted"):
        CnpgConfig.from_env({**BASE, "PGSSLMODE": mode})


def test_a_malformed_value_names_its_variable() -> None:
    with pytest.raises(ConfigError) as e:
        CnpgConfig.from_env({**BASE, "CNPG_CLIENT_POOL_MAX": "ten", "CNPG_CLIENT_STATEMENT_TIMEOUT": "30"})
    assert any(p.startswith("CNPG_CLIENT_POOL_MAX=") for p in e.value.problems)
    assert any(p.startswith("CNPG_CLIENT_STATEMENT_TIMEOUT=") for p in e.value.problems)


@pytest.mark.parametrize("host", ["a,b", "a b", "h/db", "h?x=1", "h&x", "u@h", "h#f"])
def test_a_host_that_could_carry_a_second_host_or_a_parameter_is_refused(host: str) -> None:
    with pytest.raises(ConfigError):
        CnpgConfig.from_env({**BASE, "PGHOST": host})
    CnpgConfig.from_env({**BASE, "PGHOST": "::1"})


def test_limits_are_validated() -> None:
    ok = CnpgConfig(host="h", database="d", user="u", ssl_root_cert="/ca")
    ok.validate()
    for bad in (
        ok.with_(pool_max=0),
        ok.with_(pool_min=11),
        ok.with_(connect_timeout=0),
        ok.with_(max_conn_lifetime=0),
        ok.with_(retry=RetryPolicy(attempts=0)),
        ok.with_(retry=RetryPolicy(initial_delay=10, max_delay=0.1)),
    ):
        with pytest.raises(ConfigError):
            bad.validate()


def test_durations() -> None:
    assert parse_duration("0") == 0
    assert parse_duration("500ms") == 0.5
    assert parse_duration("1.5s") == 1.5
    assert parse_duration("5m") == 300
    assert parse_duration("1h") == 3600
    for s in ("", "5", "5d", "-1s", "s"):
        with pytest.raises(ValueError):
            parse_duration(s)


def test_neither_the_password_nor_the_key_appears_in_a_string_form() -> None:
    c = CnpgConfig(
        host="h",
        database="d",
        user="u",
        ssl_root_cert="/ca",
        password="hunter2-secret",
        ssl_cert="/c",
        ssl_key="/k",
    )
    for shown in (repr(c), str(c), f"{c}"):
        assert "hunter2-secret" not in shown
        assert "<redacted>" in shown
        assert "sslmode='verify-full'" in shown


def test_connection_parameters_hold_the_paths_and_a_fresh_password(tmp_path) -> None:  # type: ignore[no-untyped-def]
    pw = tmp_path / "pw"
    pw.write_text("first\n")
    c = CnpgConfig(host="h", database="d", user="u", ssl_root_cert="/ca", password_file=str(pw))
    p1 = connection_params(c)
    assert p1["password"] == "first"
    assert p1["sslmode"] == "verify-full"
    assert p1["sslrootcert"] == "/ca"
    assert p1["ssl_min_protocol_version"] == "TLSv1.2"
    assert p1["options"] == "-c statement_timeout=30000 -c idle_in_transaction_session_timeout=60000"
    pw.write_text("second\n")
    assert connection_params(c)["password"] == "second"
