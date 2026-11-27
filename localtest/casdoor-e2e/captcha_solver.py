
import zlib, struct, re, sys, base64
def solve_png(png_bytes, glyph_src_path):
    data = png_bytes; pos=8; idat=b''
    while pos < len(data):
        ln=struct.unpack('>I',data[pos:pos+4])[0]
        typ=data[pos+4:pos+8]; body=data[pos+8:pos+8+ln]
        if typ==b'IHDR': width,height=struct.unpack('>II',body[:8])
        elif typ==b'IDAT': idat+=body
        pos+=12+ln
    raw=zlib.decompress(idat); stride=width*4
    rows=[]; prev=bytearray(stride); off=0
    for y in range(height):
        ft=raw[off]; off+=1
        line=bytearray(raw[off:off+stride]); off+=stride
        if ft==1:
            for i in range(4,stride): line[i]=(line[i]+line[i-4])&0xff
        elif ft==2:
            for i in range(stride): line[i]=(line[i]+prev[i])&0xff
        elif ft==3:
            for i in range(stride):
                a=line[i-4] if i>=4 else 0
                line[i]=(line[i]+(a+prev[i])//2)&0xff
        elif ft==4:
            for i in range(stride):
                a=line[i-4] if i>=4 else 0; b=prev[i]; c=prev[i-4] if i>=4 else 0
                p=a+b-c; pa,pb,pc=abs(p-a),abs(p-b),abs(p-c)
                pr=a if (pa<=pb and pa<=pc) else (b if pb<=pc else c)
                line[i]=(line[i]+pr)&0xff
        rows.append(bytes(line)); prev=line
    # A=0xff 且不是背景/噪点灰 ⇒ 字符
    BG=(242,245,249)
    def is_char(x,y):
        p=rows[y]; r,g,b,a=p[x*4],p[x*4+1],p[x*4+2],p[x*4+3]
        return a==255 and abs(r-BG[0])+abs(g-BG[1])+abs(b-BG[2])>120
    def col_cnt(x): return sum(1 for y in range(height) if is_char(x,y))
    body_cols=[x for x in range(width) if col_cnt(x)>0]
    if not body_cols: return None
    segs=[]; cur=[body_cols[0]]
    for x in body_cols[1:]:
        if x-cur[-1]<=1: cur.append(x)
        else: segs.append((cur[0],cur[-1])); cur=[x]
    segs.append((cur[0],cur[-1]))
    if len(segs)!=4: return None
    src=open(glyph_src_path).read()
    m=re.search(r'var glyph5x7 = map\[byte\]\[5\]byte\{(.*?)\n\}', src, re.S)
    glyphs={}
    for mm in re.finditer(r"(0x[0-9a-fA-F]+|'.'):\s*\{([^}]*)\}", m.group(1)):
        key=mm.group(1); ch=key[1] if key.startswith("'") else chr(int(key,16))
        vals=[int(v.strip(),0) for v in mm.group(2).split(',') if v.strip()]
        if len(vals)==5: glyphs[ch]=vals
    def extract(x0,x1):
        pts=[(x,y) for y in range(height) for x in range(x0,x1+1) if is_char(x,y)]
        xs=[p[0] for p in pts]; ys=[p[1] for p in pts]
        bx0,bx1,by0,by1=min(xs),max(xs),min(ys),max(ys)
        grid=set()
        for (x,y) in pts:
            gx=int((x-bx0)*5/(bx1-bx0+1)); gy=int((y-by0)*7/(by1-by0+1))
            grid.add((min(gx,4),min(gy,6)))
        return grid
    def tpl(ch):
        g=glyphs[ch]; pts=set()
        for col in range(5):
            for row in range(7):
                if g[col]&(1<<row): pts.add((col,row))
        return pts
    answer=''; scores=[]
    for (x0,x1) in segs:
        obs=extract(x0,x1)
        s,c=sorted(((len(obs&tpl(ch))/len(obs|tpl(ch)), ch) for ch in glyphs), reverse=True)[0]
        answer+=c; scores.append(round(s,2))
    return (answer, scores) if all(s>=0.75 for s in scores) else None
