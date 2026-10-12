# Embedded in downloaded scripts. Only TOOL and CONFIG_HEX are supplied by the UI.
import json
import os
import tempfile
from pathlib import Path


def fail(message):
    raise SystemExit(message + '；未修改配置。')


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail('配置存在重复键')
        result[key] = value
    return result


def merge(old, new):
    result = dict(old) if isinstance(old, dict) else {}
    for key, value in new.items():
        result[key] = merge(result.get(key), value) if isinstance(value, dict) else value
    return result


def identify(providers, incoming_id, incoming):
    matches = [key for key, value in providers.items() if isinstance(value, dict)
               and (key == incoming_id or value.get('name') == incoming.get('name'))]
    if len(matches) > 1:
        fail('存在多个同名供应商，请先在客户端确认保留项')
    if not matches:
        return incoming_id
    key = matches[0]
    old = providers[key]
    if (old.get('name', key) != incoming['name'] or
            old.get('options', {}).get('baseURL', '').rstrip('/') != incoming['options']['baseURL'].rstrip('/')):
        fail('同名或同 ID 供应商指向其他地址，请使用新的供应商名称')
    return key


def update_json(original, content):
    incoming = json.loads(content, object_pairs_hook=unique_object)
    providers = incoming.get('provider')
    if not isinstance(providers, dict) or len(providers) != 1:
        fail('生成的 provider JSON 无效')
    current = json.loads(original, object_pairs_hook=unique_object) if original.strip() else {}
    if not isinstance(current, dict):
        fail('现有 JSON 根节点不是对象')
    if TOOL == 'opencode' and 'providers' in current:
        fail('检测到 OpenCode v2 配置，请按官方文档迁移')
    existing = current.setdefault('provider', {})
    if not isinstance(existing, dict):
        fail('现有 provider 不是对象')
    provider_id, provider = next(iter(providers.items()))
    key = identify(existing, provider_id, provider)
    existing[key] = merge(existing.get(key), provider)
    return json.dumps(current, ensure_ascii=False, indent=2) + '\n'


def update_yaml(original, content):
    try:
        import yaml
    except ImportError:
        fail('需要 Python 3 和 PyYAML，请先安装 PyYAML')

    class StrictLoader(yaml.SafeLoader):
        pass

    def construct_mapping(loader, node, deep=False):
        # Reject aliases/merge keys in the provider tree rather than rewrite shared data.
        return unique_object([(loader.construct_object(k, deep=deep), loader.construct_object(v, deep=deep))
                              for k, v in node.value])

    StrictLoader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, construct_mapping)
    current = yaml.load(original, Loader=StrictLoader) if original.strip() else {}
    if current is None:
        current = {}
    if not isinstance(current, dict):
        fail('现有 YAML 根节点不是映射')
    incoming = yaml.load(content, Loader=StrictLoader).get('custom_provider')
    if not isinstance(incoming, dict) or len(incoming) != 1:
        fail('生成的 custom_provider YAML 无效')
    providers = current.get('custom_provider', {})
    if not isinstance(providers, dict):
        fail('custom_provider 不是映射')
    provider_id, provider = next(iter(incoming.items()))
    key = identify(providers, provider_id, provider)
    merged = merge(providers.get(key), provider)
    addition = yaml.safe_dump({key: merged}, allow_unicode=True, sort_keys=False)
    addition = ''.join('  ' + line if line.strip() else line for line in addition.splitlines(keepends=True))
    tree = yaml.compose(original, Loader=StrictLoader) if original.strip() else None
    section = next((v for k, v in tree.value if k.value == 'custom_provider'), None) if tree else None
    if section is None:
        updated = original + ('\n' if original and not original.endswith('\n') else '') + 'custom_provider:\n' + addition
    else:
        if not isinstance(section, yaml.MappingNode) or section.flow_style:
            fail('custom_provider 必须使用块映射格式')
        # Keep other providers and root settings byte-for-byte, including comments.
        lines = original.splitlines(keepends=True)
        entries = section.value
        index = next((i for i, (k, _) in enumerate(entries) if k.value == str(key)), None)
        if index is None:
            end = section.end_mark.line + (1 if section.end_mark.column else 0)
            lines.insert(end, addition)
        else:
            start = entries[index][0].start_mark.line
            end = entries[index + 1][0].start_mark.line if index + 1 < len(entries) else section.end_mark.line + (1 if section.end_mark.column else 0)
            lines[start:end] = [addition]
        updated = ''.join(lines)
    # Reparse the final document before any backup or write.
    checked = yaml.load(updated, Loader=StrictLoader)
    expected = merge(current, {'custom_provider': {key: merged}})
    if checked != expected:
        fail('合并结果与原配置不一致')
    return updated


def private_write(path, data, prefix):
    fd, name = tempfile.mkstemp(prefix=prefix, dir=str(path.parent))
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(name, 0o600)
        return name
    except BaseException:
        os.unlink(name)
        raise


def main():
    os.umask(0o077)
    if TOOL == 'zcode':
        root = Path(os.environ.get('APPDATA', str(Path.home() / 'AppData' / 'Roaming'))) if os.name == 'nt' else Path.home()
        target = root / ('zcode/v2/config.json' if os.name == 'nt' else '.zcode/v2/config.json')
    elif TOOL == 'opencode':
        target = Path(os.environ.get('XDG_CONFIG_HOME') or str(Path.home() / '.config')) / 'opencode/opencode.json'
        if os.environ.get('OPENCODE_CONFIG') or target.with_suffix('.jsonc').exists():
            fail('检测到 OpenCode 自定义路径或 JSONC，请手动合并所生成的片段')
    elif TOOL == 'minimax_code':
        if any(os.environ.get(key) for key in ('MINIMAX_DATA_DIR', 'MINIMAX_PROFILE', 'MAVIS_DATA_DIR')):
            fail('检测到 MiniMax 自定义配置目录或 profile，请手动合并所生成的片段')
        target = Path.home() / '.minimax/config.yaml'
    else:
        fail('该客户端需要使用应用设置界面')
    target.parent.mkdir(parents=True, exist_ok=True)
    lock = target.with_name('config.yaml.lock' if TOOL == 'minimax_code' else target.name + '.gateway-setup.lock')
    try:
        lock.mkdir()
    except FileExistsError:
        fail('配置锁已存在，请关闭配置编辑后重试')
    temp_name = None
    try:
        if target.is_symlink():
            fail('配置文件是符号链接，请手动确认目标')
        original = target.read_bytes() if target.exists() else None
        text = original.decode('utf-8') if original is not None else ''
        content = bytes.fromhex(CONFIG_HEX).decode('utf-8')
        try:
            updated = update_yaml(text, content) if TOOL == 'minimax_code' else update_json(text, content)
        except Exception:
            fail('配置无法安全解析或合并')
        data = updated.encode('utf-8')
        temp_name = private_write(target, data, target.name + '.tmp.')
        if (target.read_bytes() if target.exists() else None) != original:
            fail('配置已被其他进程修改，请重试')
        if original is not None:
            backup = private_write(target, original, target.name + '.backup.')
            print('备份已创建:', backup)
        os.replace(temp_name, target)
        temp_name = None
        print('供应商配置已写入:', target)
        if os.name == 'nt':
            print('请通过 Windows 文件权限限制配置目录访问；POSIX 模式不能替代 Windows ACL。')
    finally:
        if temp_name is not None:
            os.unlink(temp_name)
        lock.rmdir()


if __name__ == '__main__':
    main()
