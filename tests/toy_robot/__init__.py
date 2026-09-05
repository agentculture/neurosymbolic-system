"""The toy robot fixture: a third plant, neither Reachy Mini nor MicroDuck.

`adaptor.toml` and `rules.toml` are the only two files a fresh consumer writes
(spec h16/h4); `client.py` is the stdlib fixture consumer that drives them
through the built engine. `tests/test_e2e_toy_robot.py` is what asserts on all
three.
"""
