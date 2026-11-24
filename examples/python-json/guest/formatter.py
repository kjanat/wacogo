import json

from componentize_py_types import Err
from wit_world import exports


class Formatter(exports.Formatter):
    def format_json(self, source: bytes) -> bytes:
        try:
            value = json.loads(source.decode("utf-8"))
            return (json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n").encode("utf-8")
        except (ValueError, RecursionError) as error:
            raise Err(str(error)) from None
