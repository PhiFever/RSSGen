"""路由自动发现与注册"""

import importlib
import pkgutil
from pathlib import Path

from RSSGen.core.route import Route

# 路由注册表: {route_name: RouteClass}
_registry: dict[str, type[Route]] = {}


def register_route(cls: type[Route]) -> type[Route]:
    """装饰器：手动注册路由"""
    _registry[cls.name] = cls
    return cls


def discover_routes() -> dict[str, type[Route]]:
    """扫描 routes/ 目录，自动收集所有 Route 子类"""
    package_path = Path(__file__).parent
    for module_info in pkgutil.iter_modules([str(package_path)]):
        if module_info.name.startswith("_"):
            continue
        module = importlib.import_module(f"RSSGen.routes.{module_info.name}")
        for attr_name in dir(module):
            attr = getattr(module, attr_name)
            if (
                isinstance(attr, type)
                and issubclass(attr, Route)
                and attr is not Route
                and getattr(attr, "name", "")
            ):
                _registry[attr.name] = attr
    return _registry


def get_registry() -> dict[str, type[Route]]:
    return _registry
